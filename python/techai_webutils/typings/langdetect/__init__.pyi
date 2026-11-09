# Local stub: the subset of ``langdetect`` this package uses (it ships no type hints).

LangDetectException: type[Exception]

class DetectorFactory:
    seed: int

class Language:
    lang: str
    prob: float

def detect(text: str) -> str: ...
def detect_langs(text: str) -> list[Language]: ...
