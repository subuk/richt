#!/usr/bin/env python3
"""Check generated HTML links, assets, and fragment targets without network access."""

import argparse
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit


class Page(HTMLParser):
    def __init__(self, path):
        super().__init__(convert_charrefs=True)
        self.ids = set()
        self.links = []
        self.feed(path.read_text(encoding="utf-8"))

    def handle_starttag(self, tag, attributes):
        attributes = dict(attributes)
        if attributes.get("id"):
            self.ids.add(attributes["id"])
        if tag == "a" and attributes.get("name"):
            self.ids.add(attributes["name"])
        for name in ("href", "src"):
            if attributes.get(name):
                self.links.append((self.getpos()[0], attributes[name]))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path, nargs="?", default=Path("website/public"))
    parser.add_argument("--base-url", default="http://localhost:1313/", help="Public base URL, including any project path")
    args = parser.parse_args()
    base = urlsplit(args.base_url)
    prefix = base.path.rstrip("/") + "/"
    root = args.directory.resolve()
    pages = {path: Page(path) for path in root.rglob("*.html")}
    if not pages:
        parser.error(f"no generated HTML found in {root}")
    errors = []
    checked = 0
    for source, page in sorted(pages.items()):
        for line, link in page.links:
            url = urlsplit(link)
            if (url.scheme or url.netloc) and (url.scheme, url.netloc) != (base.scheme, base.netloc):
                continue
            checked += 1
            if not url.path:
                target = source
            elif url.path.startswith("/"):
                if not url.path.startswith(prefix):
                    errors.append(f"{source.relative_to(root)}:{line}: {link}: outside the site base path")
                    continue
                target = root / unquote(url.path[len(prefix):])
            else:
                target = source.parent / unquote(url.path)
            target = target.resolve()
            if not target.is_relative_to(root):
                reason = "escapes the generated site"
            else:
                if target.is_dir():
                    target = target / "index.html"
                if not target.is_file():
                    reason = "missing target"
                elif url.fragment and target in pages and unquote(url.fragment) not in pages[target].ids:
                    reason = "missing fragment"
                else:
                    continue
            errors.append(f"{source.relative_to(root)}:{line}: {link}: {reason}")
    if errors:
        for error in errors:
            print(error)
        raise SystemExit(1)
    print(f"Checked {checked} local links and assets across {len(pages)} HTML pages")


if __name__ == "__main__":
    main()
