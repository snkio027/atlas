import copy
import unittest
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

    def test_authorization_result_is_version_bound_and_fail_closed(self):
        tx = 'atlas-probe-forbidden'
        text = "Failed to describe transaction state of transactional-id `atlas-probe-forbidden`: FindCoordinator request for transactionalId `CoordinatorKey(idValue='atlas-probe-forbidden', type=TRANSACTION)` failed due to authorization failure. Enable debug logging for additional detail.\n"
        good = {'exitCode': 1, 'stdout': text, 'stderr': ''}
        self.assertTrue(transactional_denial(good, tx)['transactionalIDAuthorizationDenied'])
        bad = []
        for output in ['', text.replace(tx, 'other'), 'TransactionalIdNotFoundException\n', 'TimeoutException\n', text + 'unexpected\n']:
            x = copy.deepcopy(good); x['stdout'] = output; bad.append(x)
        for code in [0, 124, 137, -1]:
            x = copy.deepcopy(good); x['exitCode'] = code; bad.append(x)
        for value in bad:
            with self.subTest(value=value), self.assertRaises(ValueError):transactional_denial(value, tx)


if __name__ == '__main__':unittest.main()
