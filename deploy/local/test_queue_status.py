import unittest
from queue_status import summarize


class QueueStatusTests(unittest.TestCase):
    def test_queue_and_counters(self):
        result = summarize(
            'otelcol_exporter_queue_size{exporter="otlp/out",data_type="logs"} 5\n'
            'otelcol_exporter_queue_capacity{exporter="otlp/out",data_type="logs"} 10\n'
            'otelcol_exporter_send_failed_log_records_total{exporter="otlp/out"} 3\n')
        queue = next(row for row in result['exporters'] if row['signal'] == 'logs')
        self.assertEqual(queue['utilization'], 0.5)
        counter = next(row for row in result['exporters'] if row['signal'] is None)
        self.assertEqual(counter['logs_send_failed_total'], 3)

    def test_missing_and_ambiguous_metrics(self):
        self.assertEqual(summarize('# no queue metrics')['exporters'], [])
        result = summarize(
            'otelcol_exporter_queue_size{exporter="otlp",data_type="logs",instance="a"} 1\n'
            'otelcol_exporter_queue_size{exporter="otlp",data_type="logs",instance="b"} 2\n')
        self.assertFalse(result['exporters'][0]['queue_observed'])
        self.assertIsNone(result['exporters'][0]['utilization'])
        self.assertEqual(result['exporters'][0]['ambiguous_metrics'], ['depth'])

    def test_bad_values_and_labels(self):
        for value in ('NaN', '+Inf', '-1'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                summarize('otelcol_exporter_queue_size{exporter="otlp"} '+value)
        with self.assertRaises(ValueError):
            summarize('otelcol_exporter_queue_size{exporter="one",exporter="two"} 1')


if __name__ == '__main__':
    unittest.main()
