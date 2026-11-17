"""Identity and bundle hash of the factory tick loop in herdr-core (kept alive by the sheepdog daemon)."""

import hashlib

from .base import bundle


SCHEDULER = "sheepdog-factory-tick"


def script_hash():
    return hashlib.sha1(bundle()).hexdigest()
