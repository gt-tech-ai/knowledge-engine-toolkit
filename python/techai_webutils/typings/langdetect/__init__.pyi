# Minimal local stub for langdetect (ships no type information): only the surface
# techai_webutils uses.

# langdetect's exception class is an Exception subclass whose name lacks the Error
# suffix; declaring it as a class here would trip pep8-naming on a name we do not own.
LangDetectException: type[Exception]

class DetectorFactory:
    seed: int | None

# One candidate language and its probability (the items detect_langs returns).
class Language:
    lang: str
    prob: float

def detect(text: str) -> str: ...
def detect_langs(text: str) -> list[Language]: ...
