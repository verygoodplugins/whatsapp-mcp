"""Phone-number matching that tolerates how people actually type numbers.

The same Brazilian mobile can be written with or without the country code
(55), with or without the area code (DDD), and with or without the "9" that
was prepended to mobile subscriber numbers; WhatsApp itself stores some
contacts in the older 8-digit form. A typed number therefore expands into the
set of stored forms it could refer to.
"""

import re

MIN_PHONE_DIGITS = 8
BRAZIL_COUNTRY_CODE = "55"


def digits_only(value: str) -> str:
    return re.sub(r"\D", "", value)


def looks_like_phone(value: str) -> bool:
    return len(digits_only(value)) >= MIN_PHONE_DIGITS


def phone_matches(query: str, stored_phone: str) -> bool:
    digits = digits_only(query)
    if len(digits) < MIN_PHONE_DIGITS:
        return False
    return any(stored_phone.endswith(variant) for variant in _number_variants(digits))


def _number_variants(digits: str) -> set[str]:
    variants = {digits}
    national = _brazilian_national_number(digits)
    if national:
        area_code, subscriber = national[:2], national[2:]
        variants |= {BRAZIL_COUNTRY_CODE + area_code + s for s in _subscriber_variants(subscriber)}
    else:
        variants |= _subscriber_variants(digits)
    return variants


def _brazilian_national_number(digits: str) -> str | None:
    if digits.startswith(BRAZIL_COUNTRY_CODE) and len(digits) in (12, 13):
        return digits[len(BRAZIL_COUNTRY_CODE) :]
    if len(digits) in (10, 11):
        return digits
    return None


def _subscriber_variants(subscriber: str) -> set[str]:
    if len(subscriber) == 9 and subscriber.startswith("9"):
        return {subscriber, subscriber[1:]}
    if len(subscriber) == 8 and subscriber[0] in "6789":
        return {subscriber, "9" + subscriber}
    return {subscriber}
