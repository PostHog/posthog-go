import unittest

from check_reports import PROFILES, check_report


def report_for(profile):
    count = PROFILES[profile]
    suite = "Capture" if profile.startswith("v0-") else "Capture_V1"
    codec = profile.split("-")[1]
    capture = ["Non Utc Event Timestamp Is Converted To Utc", f"Sends {codec.title()} Content Encoding"]
    capture += [f"Capture {index}" for index in range(count - len(capture))]
    flags = [
        "Retries Flags On 502",
        "Retries Flags On 504",
        "Disable Geoip False Propagates As Geoip Disable False",
        "Disable Geoip Omitted Defaults To False",
    ]
    flags += [f"Flag {index}" for index in range(17 - len(flags))]
    report = f"**{count + 15}/{count + 17}** tests passed, **2** failed\n"
    for name, rows in [(suite, capture), ("Feature_Flags", flags)]:
        report += f"## {name} Tests\n"
        for row in rows:
            status = "❌" if row.startswith("Disable Geoip") else "✅"
            report += f"| {row} | {status} | 1ms |\n"
    return report


class CheckReportsTests(unittest.TestCase):
    def test_all_profile_inventories_accept_advisory_failures(self):
        for profile, count in PROFILES.items():
            self.assertIn(f"selected={count+17}", check_report(profile, report_for(profile)))

    def test_missing_empty_truncated_or_wrong_inventory_fails(self):
        report = report_for("v1-deflate")
        for invalid in ["", report[:100], report.replace("Capture 0", "Capture 1"),
                        report.replace("111", "0"), report.replace("Sends Deflate", "Sends Gzip"),
                        report.replace("Non Utc Event Timestamp Is Converted To Utc", "Different Test")]:
            with self.assertRaises(ValueError):
                check_report("v1-deflate", invalid)


if __name__ == "__main__":
    unittest.main()
