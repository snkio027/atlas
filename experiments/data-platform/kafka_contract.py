"""Locked Kafka CLI boundary; human-readable errors are not a stable protocol.

Only this version-specific adapter interprets CLI output. A version change must
update fixtures and command-capability checks before any create/produce step.
"""
import re

IMAGE = 'quay.io/strimzi/kafka:1.3.0-kafka-4.3.1@sha256:1f189b1f0f32684302d8b018b1d21b411e2469a9c465fbdc18fa3599cb5bbdad'


def validate_cli(image, consumer_help):
    if image != IMAGE or not re.search(r'(?m)^\s*--command-config\b', consumer_help) or not re.search(r'(?m)^\s*--command-property\b', consumer_help):
        raise ValueError('Kafka executable does not implement the reviewed command contract')


def consumer(server, properties, topic):
    if not re.fullmatch(r'[a-z0-9.-]+:[0-9]+', server) or not re.fullmatch(r'[a-z0-9-]+', topic) or properties != '/tmp/client.properties':
        raise ValueError('unexpected Kafka probe target')
    return ['/opt/kafka/bin/kafka-console-consumer.sh', '--bootstrap-server', server,
            '--command-config', properties, '--command-property', 'enable.auto.commit=false',
            '--topic', topic, '--partition', '0', '--offset', 'earliest', '--max-messages', '1', '--timeout-ms', '15000']


def consumed(result, message):
    if result != {'exitCode': 0, 'stdout': message + '\n', 'stderr': 'Processed a total of 1 messages\n'}:
        raise ValueError('Kafka consumer did not return the exact expected record')


def transactional_denial(result, transactional_id):
    if not re.fullmatch(r'[a-z0-9-]+', transactional_id):
        raise ValueError('invalid transactional ID')
    expected = (f"Failed to describe transaction state of transactional-id `{transactional_id}`: "
                f"FindCoordinator request for transactionalId `CoordinatorKey(idValue='{transactional_id}', type=TRANSACTION)` "
                "failed due to authorization failure. Enable debug logging for additional detail.\n")
    # TransactionsCommand.printErrorAndExit uses Throwable.getMessage(), not its
    # Java class name. Timeout, missing-ID, transport and unknown errors fail.
    if result != {'exitCode': 1, 'stdout': expected, 'stderr': ''}:
        raise ValueError('not the reviewed transactional-ID authorization denial')
    return {'transactionalIDAuthorizationDenied': True}
