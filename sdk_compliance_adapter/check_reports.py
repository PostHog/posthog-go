"""Verify the pinned harness's Markdown inventory, not advisory job conclusions."""

import re
import sys
from pathlib import Path

PROFILES = {"v0-gzip": 30, "v1-gzip": 95, "v1-deflate": 94, "v1-br": 94, "v1-zstd": 94}


def check_report(profile: str, report: str) -> str:
    capture_count = PROFILES[profile]
    suite = "capture" if profile.startswith("v0-") else "capture_v1"
    expected = {suite: capture_count, "feature_flags": 17}
    sections = re.split(r"^## (Capture|Capture_V1|Feature_Flags) Tests\n", report, flags=re.M)
    observed = {}
    for name, section in zip(sections[1::2], sections[2::2]):
        rows = re.findall(r"^\| (.+?) \| [✅❌] \| \d+ms \|$", section, re.M)
        observed[name.lower()] = len(rows)
        if len(rows) != len(set(rows)):
            raise ValueError(f"{profile}: duplicate test rows")
    if observed != expected:
        raise ValueError(f"{profile}: expected {expected}, got {observed}")
    total = capture_count + 17
    summary = re.search(r"\*\*(\d+)/(\d+)\*\* tests passed", report)
    if not summary or int(summary[2]) != total:
        raise ValueError(f"{profile}: missing or unexpected summary total (expected {total})")
    passed_rows = len(re.findall(r"^\| .+? \| ✅ \| \d+ms \|$", report, re.M))
    if passed_rows != int(summary[1]):
        raise ValueError(f"{profile}: summary pass count differs from test rows")
    required = [
        "non_utc_event_timestamp_is_converted_to_utc",
        "retries_flags_on_502",
        "retries_flags_on_504",
        "disable_geoip_false_propagates_as_geoip_disable_false",
        "disable_geoip_omitted_defaults_to_false",
    ]
    if suite == "capture_v1":
        required.append(f"sends_{profile.split('-')[1]}_content_encoding")
    for name in required:
        if name.replace("_", " ").title() + " |" not in report:
            raise ValueError(f"{profile}: missing test {name}")
    passed = int(summary[1])
    return f"{profile}: selected={total}, passed={passed}, failed={total-passed}"


def main() -> None:
    root = Path(sys.argv[1])
    for profile in PROFILES:
        path = root / f"sdk-compliance-report-{profile}" / "sdk-compliance-report.md"
        print(check_report(profile, path.read_text()))


if __name__ == "__main__":
    main()
