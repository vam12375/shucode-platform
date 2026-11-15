import unittest

from render_edgeone_policy import parse_networks, render


class EdgeOnePolicyTests(unittest.TestCase):
    def test_rejects_invalid_and_unbounded_sources(self):
        for value in ("", "0.0.0.0/0", "::/0", "127.0.0.1", "10.0.0.0/8", "224.0.0.0/4", "example.com", "1.1.1.1; allow all;", "1.1.1.1/24"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                parse_networks(value)

    def test_uses_original_peer_and_separate_client_header_trust(self):
        # These public ranges are synthetic test inputs, not an EdgeOne origin list.
        networks = parse_networks("1.1.1.0/24\n2606:4700::/32\n1.1.1.0/24\n# comment")
        self.assertEqual(len(networks), 2)
        files = render(networks, "X-Forwarded-For")
        self.assertIn("geo $realip_remote_addr", files["edgeone-origin-geo.conf"])
        self.assertIn("default 0;", files["edgeone-origin-geo.conf"])
        self.assertIn("return 444", files["edgeone-origin-gate.conf"])
        self.assertIn("set_real_ip_from 1.1.1.0/24;", files["edgeone-realip.conf"])
        self.assertNotIn("allow all", "".join(files.values()))
        with self.assertRaises(ValueError):
            render(networks, "X-Forwarded-For; allow all")


if __name__ == "__main__":
    unittest.main()
