#!/usr/bin/env python3
"""Verify synthetic SDK data in the local Elastic stack; never print secrets."""
from pathlib import Path
from datetime import datetime,timedelta,timezone
import base64,json,os,subprocess,sys,time,urllib.request,urllib.parse,urllib.error

LOCAL=Path(__file__).resolve().parent
REPO=LOCAL.parents[1]

def load_settings():
    values={}
    for line in (LOCAL/'.env').read_text().splitlines():
        if '=' in line and not line.lstrip().startswith('#'):
            k,v=line.split('=',1);values[k]=v.strip().strip('"').strip("'")
    return values

class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request(path,body=None,kibana=False):
    values=load_settings()
    auth=base64.b64encode(('elastic:'+values['ES_LOCAL_PASSWORD']).encode()).decode()
    headers={'Authorization':'Basic '+auth,'Content-Type':'application/json'}
    if kibana:headers.update({'kbn-xsrf':'local-check','x-elastic-internal-origin':'Kibana','elastic-api-version':'1'})
    port=values['KIBANA_LOCAL_PORT'] if kibana else values['ES_LOCAL_PORT']
    req=urllib.request.Request('http://127.0.0.1:'+port+path,headers=headers,data=None if body is None else json.dumps(body).encode())
    with urllib.request.build_opener(_NoRedirect()).open(req,timeout=20) as response:
        body=response.read(2*1024*1024+1)
        if len(body)>2*1024*1024:raise RuntimeError('local verification response exceeds 2 MiB')
        return json.loads(body)

def wait_for(fn,timeout=90):
    end=time.monotonic()+timeout
    while True:
        try:
            result=fn()
            if result:return result
        except urllib.error.HTTPError as error:
            if error.code not in (404,429,500,502,503,504):raise
        except urllib.error.URLError:
            pass
        if time.monotonic()>=end:raise RuntimeError('timed out waiting for local ingestion/aggregation')
        time.sleep(1)

def trace_records(trace_id):
    spans=request('/traces-generic.otel-default/_search',{'size':10,'query':{'term':{'trace.id':trace_id}},'_source':False,'fields':['span.id','parent.id','service.name']})['hits']['hits']
    logs=request('/logs-generic.otel-default/_search',{'size':10,'query':{'term':{'trace.id':trace_id}},'_source':False,'fields':['span.id','service.name','log.level']})['hits']['hits']
    return (spans,logs) if len(spans)==3 and len(logs)==2 else None

def main():
    health=request('/_cluster/health')
    license_type=request('/_license')['license']['type']
    kibana=wait_for(lambda:'available' if request('/api/status',kibana=True)['status']['overall']['level']=='available' else None)
    started=datetime.now(timezone.utc)
    results=[]
    for protocol in ['http/protobuf','grpc','http/json']:
        env={k:v for k,v in os.environ.items() if not k.startswith(('OTEL_','EDOT_GO_','ELASTIC_OTEL_JAVA_'))}
        env.update(OTEL_EXPORTER_OTLP_PROTOCOL=protocol,OTEL_EXPORTER_OTLP_ENDPOINT='http://127.0.0.1:'+('4317' if protocol=='grpc' else '4318'),OTEL_RESOURCE_ATTRIBUTES='deployment.environment.name=local,service.version=0.3.0',EDOT_GO_RUNTIME_METRICS_ENABLED='false')
        process=subprocess.run(['go','run','./examples/localdemo'],cwd=REPO,env=env,capture_output=True,text=True,timeout=60)
        if process.returncode:raise RuntimeError('synthetic demo failed for '+protocol+'; inspect local collector logs')
        demo=json.loads(process.stdout)
        if demo['http_statuses']!=[500,200,200,500,200,200]:raise RuntimeError('unexpected synthetic HTTP statuses')
        for key in ['frontend','backend']:
            diag=demo[key]
            if any(diag[s]['FailedBatches'] or not diag[s]['SuccessfulItems'] for s in ['Traces','Metrics','Logs']):raise RuntimeError('missing or failed signal export')
        for index,tid in enumerate(demo['trace_ids']):
            spans,logs=wait_for(lambda:trace_records(tid),30)
            by_id={h['fields']['span.id'][0]:h['fields'] for h in spans}
            back=next(h['fields'] for h in spans if h['fields']['service.name']==['edot-go-local-backend'])
            parent=by_id[back['parent.id'][0]]
            if parent['service.name']!=['edot-go-local-frontend']:raise RuntimeError('missing frontend/backend parent link')
            expected_level='ERROR' if index%3==0 else 'INFO'
            if any(h['fields']['span.id'][0] not in by_id or h['fields']['log.level']!=[expected_level] for h in logs):raise RuntimeError('log correlation or severity mismatch')
        results.append({'protocol':protocol,'verified_traces':6,'verified_spans':18,'correlated_logs':12,'synthetic_http_500':2})
        print(json.dumps(results[-1]),flush=True)
    # Verify application counters, not merely exporter/receiver acknowledgement.
    metric_counts={}
    for component in ['frontend','backend']:
        name='edot-go-local-'+component
        query={'size':0,'query':{'bool':{'filter':[{'term':{'service.name':name}},{'exists':{'field':'metrics.localdemo.'+component+'.requests'}},{'range':{'@timestamp':{'gte':started.isoformat()}}}]}}}
        def count():
            n=request('/metrics-generic.otel-default/_search',query)['hits']['total']['value']
            return n if n>=3 else None
        metric_counts[name]=wait_for(count,30)
    def services():
        now=datetime.now(timezone.utc)
        params={'start':(now-timedelta(minutes=30)).isoformat(),'end':now.isoformat(),'environment':'ENVIRONMENT_ALL','kuery':'','documentType':'transactionMetric','rollupInterval':'1m','useDurationSummary':'true','probability':1}
        data=request('/internal/apm/services?'+urllib.parse.urlencode(params),kibana=True)
        names={x['serviceName'] for x in data['items']}
        return data['items'] if {'edot-go-local-frontend','edot-go-local-backend'}.issubset(names) else None
    items=wait_for(services,90)
    summary={'cluster_status':health['status'],'license':license_type,'kibana_status':kibana,'protocols':results,'metric_documents':metric_counts,'apm_services':[{'service':x['serviceName'],'error_rate':x.get('transactionErrorRate')} for x in items],'service_map_ui_tested':False,'cloud_managed_otlp_tested':False}
    print(json.dumps(summary,indent=2))

if __name__=='__main__':
    try:main()
    except Exception as error:
        print('Local integration verification failed:',str(error),file=sys.stderr)
        sys.exit(1)
