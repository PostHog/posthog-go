#!/usr/bin/env python3
"""Generate the Markdown SDK reference with gomarkdoc (pinned, never a module dependency).

gomarkdoc renders every symbol. This script only groups the symbols by the
`posthog-capability: <slug>` line in their doc comment, orders them, and writes
front matter. Usage: generate_markdown.py [--source DIR] [--out DIR] [--latest] [--experimental]
"""
import argparse
import os
import re
import subprocess
import sys
import tempfile

GOMARKDOC = "github.com/princjef/gomarkdoc/cmd/gomarkdoc@v1.1.0"
REFERENCE_ID = "posthog-go"
NAME = "PostHog Go SDK"
CLIENT_CLASSES = ["FeatureFlagEvaluations"]  # public classes that get their own H2

# Capabilities this SDK implements, in sdk-specs README order (setup, capture, identify first).
CAPABILITIES = [
    ("setup", "Setup"), ("capture", "Capture"), ("identify", "Identify"), ("alias", "Alias"),
    ("capture-exception", "Capture exception"), ("flush", "Flush"),
    ("get-feature-flag", "Get feature flag"), ("get-feature-flag-payload", "Get feature flag payload"),
    ("get-feature-flag-result", "Get feature flag result"), ("get-feature-flags", "Get feature flags"),
    ("evaluate-flags", "Evaluate flags"), ("group-identify", "Group identify"),
    ("is-feature-enabled", "Is feature enabled"), ("reload-feature-flags", "Reload feature flags"),
    ("shutdown", "Shutdown"), ("before-send-hook", "Before send hook"),
]
TAG = re.compile(r"^posthog\\?-capability: (\S+)$")  # gomarkdoc escapes the hyphen
HEAD = re.compile(r"^(##|###) (func|type) (?:\\\(\\?\*?(\w+)\\\) )?(?:\[(\w+)\]|(\w+))")


class Sym:
    def __init__(self, kind, recv, name, level, body):
        self.kind, self.recv, self.name, self.level, self.body = kind, recv, name, level, body
        self.tag = None
        self.children = []

    @property
    def title(self):
        return f"{self.recv}.{self.name}" if self.recv else self.name


def run_gomarkdoc(source):
    out = os.path.join(tempfile.mkdtemp(), "gomarkdoc.md")
    subprocess.run(["go", "run", GOMARKDOC, "--output", out, "."], cwd=source, check=True)
    return open(out).read()


def split_symbols(text):
    """Split gomarkdoc output into extra blocks (constants, variables) and symbols."""
    chunks, cur = [], None
    in_fence = False
    for line in text.split("\n"):
        if line.startswith("```"):
            in_fence = not in_fence
        if not in_fence and re.match(r"^#{1,3} ", line):
            cur = [line]
            chunks.append(cur)
        elif cur is not None and not line.startswith("<a name="):
            cur.append(line)
    syms, blocks = [], {}
    for c in chunks:
        m = HEAD.match(c[0])
        if m:
            syms.append(Sym(m[2], m[3], m[4] or m[5], len(m[1]), c[1:]))
        elif c[0] in ("## Constants", "## Variables"):
            blocks[c[0][3:]] = c[1:]
    return syms, blocks


def expand_interface(sym):
    """gomarkdoc keeps interface methods inside one code block; split them into symbols."""
    body = "\n".join(sym.body)
    m = re.search(r"```go\ntype \w+ interface \{\n(.*?)\n\}\n```", body, re.S)
    if not m:
        return []
    sym.body = body[:m.start()].split("\n")
    members, comment = [], []
    for line in m[1].split("\n"):
        line = line[4:] if line.startswith("    ") else line
        if line.startswith("//"):
            comment.append(line[2:][1:] if line[2:].startswith(" ") else line[2:])
        elif line.strip() and re.match(r"^\w+\(", line):
            name = line.split("(")[0]
            members.append(Sym("method", sym.name, name, 3, interface_body(line, comment)))
            comment = []
        else:
            comment = []
    return members


def interface_body(signature, comment):
    out, para, code = ["```go", signature, "```", ""], [], False
    for line in comment + [""]:
        if line.startswith("\t"):
            if not code:
                out += ["```go"]
                code = True
            out.append(line[1:])
            continue
        if code:
            out += ["```", ""]
            code = False
        if line.strip():
            para.append(line.strip())
        elif para:
            out += [" ".join(para), ""]
            para = []
    return out


def pull_tag(sym):
    for i, line in enumerate(sym.body):
        m = TAG.match(line.strip())
        if m:
            sym.tag = m[1].replace("\\","")
            del sym.body[i]
            while i < len(sym.body) and not sym.body[i].strip() and sym.body[i - 1:i] and not sym.body[i - 1].strip():
                del sym.body[i]
            return


def clean(lines):
    text = "\n".join(lines).strip("\n")
    text = re.sub(r"\[([^\]]+)\]\(<#[^>]*>\)", r"`\1`", text)  # gomarkdoc in-page links have no target here
    text = re.sub(r"\n{3,}", "\n\n", text)
    # MDX 1 reads raw < and { in prose as JSX: escape them outside code
    parts = re.split(r"(```.*?```|`[^`\n]*`)", text, flags=re.S)
    return "".join(p if i % 2 else re.sub(r"<(?!https?://|/?(?:details|summary|p)>)", "&lt;", p)
                   .replace("\\{", "{").replace("\\}", "}").replace("{", "&#123;").replace("}", "&#125;")
                   for i, p in enumerate(parts))


def render(sym, level):
    return f"{'#' * level} `{sym.title}`\n\n{clean(sym.body)}\n\n" + "".join(render(c, level + 1) for c in sym.children)


def slug(title):
    return re.sub(r"[^\w\- ]", "", title.lower()).replace(" ", "-")


def build(text):
    syms, blocks = split_symbols(text)
    flat, parent = [], None
    for s in syms:
        if s.level == 2:
            parent = s if s.kind == "type" else None
        elif parent is not None:  # method or constructor nested under its type
            parent.children.append(s)
            continue
        flat.append(s)
    for s in list(flat):
        if s.kind == "type" and any("interface {" in l for l in s.body):
            s.children = expand_interface(s) + s.children
    everything = []
    for s in flat:
        everything.append(s)
        everything += s.children
    for s in everything:
        pull_tag(s)
    # tagged nested symbols move out of their type
    for s in flat:
        s.children = [c for c in s.children if c.tag is None]
    sections = {slug_: [] for slug_, _ in CAPABILITIES}
    utils, classes, types = [], [], []
    for s in flat:
        if s.tag:
            sections[s.tag].append(s)
        elif s.name in CLIENT_CLASSES:
            classes.append(s)
        elif s.kind == "func":
            utils.append(s)
        else:
            types.append(s)
    for s in everything:
        if s.tag and s not in flat:
            sections[s.tag].append(s)  # tagged method/constructor
    unknown = {s.tag for s in everything if s.tag} - set(sections)
    if unknown:
        sys.exit(f"unknown capability tag(s): {sorted(unknown)}")
    return sections, utils, classes, types, blocks


def write_migration(source, out):
    """Concatenate docs/migration-v<N>.md guides, newest first, under one page."""
    import glob
    files = sorted(glob.glob(os.path.join(source, "docs", "migration-v*.md")), reverse=True,
                   key=lambda f: int(re.search(r"v(\d+)", os.path.basename(f))[1]))
    if not files:
        return
    guides, fence = [], False
    for f in files:
        for line in open(f).read().strip().split("\n"):
            fence ^= line.startswith("```")
            guides.append("#" + line if re.match(r"^#+ ", line) and not fence else line)
        guides.append("")
    fm = f'---\ntitle: "{NAME} migration guides"\nsidebar: Docs\nshowTitle: true\n---\n\n'
    with open(os.path.join(out, f"{REFERENCE_ID}-migration.md"), "w") as f:
        f.write(fm + clean(guides) + "\n")
    print("wrote", os.path.join(out, f"{REFERENCE_ID}-migration.md"))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--source", default=".")
    ap.add_argument("--out", default="references")
    ap.add_argument("--latest", action="store_true", help="also write the latest page")
    ap.add_argument("--experimental", action="store_true", help="mark the major as unreleased")
    a = ap.parse_args()
    source = os.path.abspath(a.source)
    mod = re.search(r"^module \S+?(?:/v(\d+))?$", open(os.path.join(source, "go.mod")).read(), re.M)
    major = mod[1] or "1"
    sections, utils, classes, types, blocks = build(run_gomarkdoc(source))

    key = lambda s: (s.kind == "type", s.title)
    body, toc = [], []

    def h2(title, content):
        toc.append(title)
        body.append(f"## {title}\n\n{content.strip()}\n")

    gs = open(os.path.join(source, "docs", "getting-started.md")).read().strip()
    h2("Getting started", re.sub(r"^(#+) ", r"#\1 ", gs, flags=re.M).split("\n", 2)[-1] if gs.startswith("# ") else gs)
    for slug_, title in CAPABILITIES:
        if sections[slug_]:
            h2(title, "".join(render(s, 3) for s in sorted(sections[slug_], key=key)))
    util = "".join(render(s, 3) for s in sorted(utils, key=key))
    for name in ("Constants", "Variables"):
        if name in blocks:
            util += f"### {name}\n\n{clean(blocks[name])}\n\n"
    h2("SDK utils", util)
    for c in classes:
        h2(f"`{c.name}`", clean(c.body) + "\n\n" + "".join(render(x, 3) for x in sorted(c.children, key=key)))
    h2("Types", "".join(render(s, 3) for s in sorted(types, key=key)))

    suffix = " (experimental)" if a.experimental else ""
    def page(title):
        fm = ["---", f'title: "{title}"', "sidebar: Docs", "showTitle: true", "contentMaxWidthClass: max-w-full",
              "platformLogo: go", "tableOfContents:"]
        for t in toc:
            fm += [f'  - value: "{t.replace("`", "")}"', f"    url: {slug(t)}", "    depth: 0"]
        return "\n".join(fm + ["---", ""]) + "\n" + "\n".join(body)

    os.makedirs(a.out, exist_ok=True)
    pages = {f"{REFERENCE_ID}-{major}.md": f"{NAME} {major}.x{suffix}"}
    if a.latest:
        pages[f"{REFERENCE_ID}-reference.md"] = NAME
    for fname, title in pages.items():
        with open(os.path.join(a.out, fname), "w") as f:
            f.write(page(title))
        print("wrote", os.path.join(a.out, fname))
    write_migration(source, a.out)


if __name__ == "__main__":
    main()
