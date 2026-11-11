// SPDX-License-Identifier: Apache-2.0
import io.opentelemetry.api.common.Attributes;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanContext;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.api.trace.TraceFlags;
import io.opentelemetry.api.trace.TraceState;
import io.opentelemetry.api.trace.TraceStateBuilder;
import io.opentelemetry.context.Context;
import io.opentelemetry.contrib.sampler.consistent56.ConsistentSampler;
import io.opentelemetry.sdk.trace.samplers.SamplingResult;
import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.util.Collections;

/** Produces expected results from the Java implementation using tab-separated inputs. */
public class Oracle {
  public static void main(String[] args) throws Exception {
    BufferedReader input = new BufferedReader(new InputStreamReader(System.in));
    String line;
    while ((line = input.readLine()) != null) {
      String[] fields = line.split("\t", -1);
      double ratio = Double.parseDouble(fields[0]);
      String traceId = fields[1];
      boolean root = fields[2].equals("-1");
      boolean sampled = fields[2].equals("1");
      TraceStateBuilder builder = TraceState.builder().put("vendor", "keep");
      if (!fields[3].isEmpty()) {
        builder.put("ot", fields[3]);
      }
      TraceState state = builder.build();
      Context context = Context.root();
      if (!root) {
        SpanContext parent = SpanContext.createFromRemoteParent(
            traceId, "0100000000000000",
            sampled ? TraceFlags.getSampled() : TraceFlags.getDefault(), state);
        context = context.with(Span.wrap(parent));
      }
      SamplingResult result = ConsistentSampler.parentBased(
              ConsistentSampler.probabilityBased(ratio))
          .shouldSample(context, traceId, "test", SpanKind.INTERNAL,
              Attributes.empty(), Collections.emptyList());
      String ot = result.getUpdatedTraceState(root ? TraceState.getDefault() : state).get("ot");
      System.out.println(result.getDecision() + "\t" + (ot == null ? "" : ot));
    }
  }
}
