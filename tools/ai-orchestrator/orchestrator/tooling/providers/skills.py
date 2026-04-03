class SkillsProvider:
    def __init__(self, *_args: object, **_kwargs: object) -> None:
        raise ValueError("provider.type=skills is deprecated; migrate to skill releases/skillsets or mcp_http")

    def call(self, *_args: object, **_kwargs: object) -> dict[str, object]:
        raise ValueError("provider.type=skills is deprecated; migrate to skill releases/skillsets or mcp_http")
