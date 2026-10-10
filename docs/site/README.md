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

The output uses the dixon.cx page shell and is indexable, with canonical,
Open Graph, Twitter and TechArticle JSON-LD metadata. Add any new chapter to the
`<!-- Noema -->` block of dixon.cx's `sitemap.xml`, then run
`scripts/site-seo-audit.py` there.
