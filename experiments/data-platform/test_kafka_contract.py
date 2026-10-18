import copy
import unittest
import json
from pathlib import Path
from kafka_contract import IMAGE, consumed, consumer, transactional_denial, validate_cli


class KafkaContract(unittest.TestCase):
    def test_cli_capability_preflight_and_current_arguments(self):
        validate_cli(IMAGE, '--command-config <String>\n--command-property <String>\n')
        args = consumer('kafka-kafka-bootstrap.service-kafka.svc.cluster.local:9093', '/tmp/client.properties', 'atlas-probe')
        self.assertIn('--command-config', args); self.assertIn('--command-property', args)
        self.assertNotIn('--consumer.config', args); self.assertNotIn('--consumer-property', args)
        for image, text in [(IMAGE, '--consumer.config file\n--consumer-property value'), (IMAGE.replace('4.3.1', '4.3.2'), '--command-config\n--command-property')]:
            with self.assertRaises(ValueError):validate_cli(image, text)

    def test_warning_pollution_is_not_a_record(self):
        result = {'exitCode': 0, 'stdout': 'marker\n', 'stderr': 'Processed a total of 1 messages\n'}
        consumed(result, 'marker')
        result['stdout'] = 'Warning: deprecated option\nmarker\n'
        with self.assertRaises(ValueError):consumed(result, 'marker')

    def test_captured_kubectl_authorization_denial(self):
        # Sanitized capture checked against Kafka stderr and kubectl ExitError.
        # This fixture is independent of the implementation's string builder.
        result = json.loads((Path(__file__).parent / 'fixtures/kafka-transaction-denial.json').read_text())
        self.assertTrue(transactional_denial(result, 'atlas-probe-forbidden')['transactionalIDAuthorizationDenied'])

    def test_authorization_result_is_version_bound_and_fail_closed(self):
        tx = 'atlas-probe-forbidden'
        good = json.loads((Path(__file__).parent / 'fixtures/kafka-transaction-denial.json').read_text())
        text = good['stderr']
        marker = 'command terminated with exit code 1\n'
        bad = []
        for output in ['', text.replace(tx, 'other'), 'TransactionalIdNotFoundException\n',
                       'TimeoutException\n' + marker, 'Unable to connect to the server\n',
                       text + 'unexpected\n', 'warning\n' + text,
                       text.removesuffix(marker), marker, text + marker,
                       text.replace('exit code 1', 'exit code 124')]:
            x = copy.deepcopy(good); x['stderr'] = output; bad.append(x)
        # Neither the old incorrect stdout fixture nor combined streams may pass.
        for stdout, stderr in [(text, ''), (text.removesuffix(marker), marker),
                               (text, text), ('unexpected\n', text)]:
            x = copy.deepcopy(good); x.update(stdout=stdout, stderr=stderr); bad.append(x)
        for code in [0, 124, 137, -1]:
            x = copy.deepcopy(good); x['exitCode'] = code; bad.append(x)
        for value in bad:
            with self.subTest(value=value), self.assertRaises(ValueError):transactional_denial(value, tx)
        for wrong_id in ['other', '', 'invalid id']:
            with self.subTest(transactional_id=wrong_id), self.assertRaises(ValueError):
                transactional_denial(good, wrong_id)


if __name__ == '__main__':unittest.main()
