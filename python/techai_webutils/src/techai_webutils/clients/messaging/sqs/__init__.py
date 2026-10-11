"""SQS backend for the messaging client — publisher + subscriber (aiobotocore).

Backend subpackage of ``clients/messaging``: the tier root's
``builder.py`` selects this backend. ``MessagingKind`` + a ``memory`` stub land in
.
"""

from techai_webutils.clients.messaging.sqs.sqs_publisher import SQSPublisher
from techai_webutils.clients.messaging.sqs.sqs_subscriber import SQSSubscriber

__all__ = ["SQSPublisher", "SQSSubscriber"]
