# Documentation site

`build.py` renders the documentation section from the repository's Markdown,
the OpenAPI document and the screenshots into static HTML that follows the
dixon.cx page skeleton. Authored pages live in `pages/`; a page pulls a
repository document in with `{{include path [demote=N] [keep-title]}}` and the
API page uses `{{openapi}}`.

```
python3 -m venv .venv && .venv/bin/pip install markdown
.venv/bin/python docs/site/build.py /path/to/public_html/noema
```

The output is intentionally not linked from the site navigation and carries a
`noindex` robots directive.
