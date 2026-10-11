# Minimal local stub for langdetect (ships no type information): only the surface
# techai_webutils uses.

# langdetect's exception class is an Exception subclass whose name lacks the Error
# suffix; declaring it as a class here would trip pep8-naming on a name we do not own.
LangDetectException: type[Exception]

class DetectorFactory:
    seed: int | None

def detect(text: str) -> str: ...
