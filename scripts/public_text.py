"""This repository is public, the whole of it: its code, docs and tests say each rule as itself, never where it came from.

So no planning thread, no PM, no dated rule, and no team's own name for its working lanes. Those names are matched by the
first 16 hex digits of their SHA-256, so this file doesn't spell them out either. To add one:
printf %s NAME | sha256sum | cut -c1-16.

Run from the repository's root: `python3 scripts/public_text.py [paths...]` (scripts/check.sh runs it on the tree), or
`--text` to check stdin: a pull request's title, body or review (.github/workflows/public-text-pr.yml).
"""
import hashlib, re, subprocess, sys

NAMES = set("""
3c32e58c6fc3cf62 20cca48ece26c362 f5068c4b858ead38 fe9c6bd7a66301f4 50077b782480811e
be896ac583d76627 afb42e3f1a4bce7e d419916f176e52d3 bd184cdc7aea9ef8 ab63a9b276c86bec
2ad06b22b301f873 3c00d0a9d4c2eb08 4a25962615e31ac6
d184eb5cf606a3eb d1cac6ef5a9ddbd1 a74b534355e685fc ea0edce134693c74 cb722454717333e5 6ce2da740ecd217f
""".split())
# Logins, the owner's included: named only where GitHub reads them.
LOGINS = set("""
53dd1be914bd441c f66e74c6b8883a1f 3726111e555e250c 018a7f9527b114ab 94c6cdf43ac3f929
""".split())
OWNERS = ".github/CODEOWNERS"
MONTH = r"(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*"
SAID = re.compile(
    r"kiff/work\b|\bthe PM\b|\bPM's\b|\(PM[ ,)]|\bPM [0-9]{1,2}:[0-9]{2}"
    r"|\b(review|probe|ask|point|negatives|read) (on|of)( [A-Za-z0-9/_.-]*#[0-9]+)?\)"
    rf"|\bowner(?:'s|,) [0-9]{{1,2}} {MONTH}\b"
    # A status line's own words: a hand-off (the all-caps word with an arrow) or a board's line.
    r"|\bREADY\s*(?:→|->)|\bBOARD:"
)
WORD = re.compile(r"(?<![\w-])[a-z0-9]+(?:-[a-z0-9]+)*(?![\w-])")
SKIP = {"scripts/check.sh", "scripts/public_text.py", "web/internal/handlers/skills_test.go"}
SKIP_DIRS = ("web/internal/content/decks/",)


def named(path, line):
    return SAID.search(line) or next((w for w in WORD.finditer(line.lower())
        if (h := hashlib.sha256(w.group().encode()).hexdigest()[:16]) in NAMES
        or (h in LOGINS and path != OWNERS)), None)


if sys.argv[1:] == ["--text"]:
    # A pull request's title, body or review, on stdin (.github/workflows/public-text-pr.yml).
    found = [f"line {n}: {line.strip()[:160]}" for n, line in enumerate(sys.stdin.read().splitlines(), 1) if named("", line)]
    if found:
        print("This pull request's text names a thread, a lane, the PM or a dated rule; say what changed and why:")
        print("\n".join(found))
        sys.exit(1)
    print("clean")
    sys.exit(0)

paths = sys.argv[1:] or subprocess.run(
    ["git", "ls-files", "-z"], capture_output=True, check=True).stdout.decode().split("\0")
found = []
for path in paths:
    if not path or path in SKIP or path.startswith(SKIP_DIRS):
        continue
    try:
        text = open(path, encoding="utf-8").read()
    except (UnicodeDecodeError, IsADirectoryError, FileNotFoundError):
        continue  # binary, a submodule or gone: no words to read
    for n, line in enumerate(text.splitlines(), 1):
        if named(path, line):
            found.append(f"{path}:{n}: {line.strip()[:160]}")
if found:
    print("Public text names a thread, a lane, the PM or a dated rule; say the rule itself:")
    print("\n".join(found))
    sys.exit(1)
print("clean")
