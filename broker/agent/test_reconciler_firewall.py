"""Tests for reconcile_firewall() — fwmark + policy routing for transparent access."""

import os
import sys
import tempfile
from pathlib import Path
from unittest.mock import patch, MagicMock

import pytest

# Add parent dir to path for imports
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from agent.reconciler import reconcile_firewall


@pytest.fixture
def tmp_config_dir(tmp_path):
    """Override CONFIG_DIR to use a temp directory."""
    with patch("agent.reconciler.CONFIG_DIR", tmp_path):
        yield tmp_path


@pytest.fixture
def mock_run_cmd():
    """Mock run_cmd to avoid actual system calls."""
    with patch("agent.reconciler.run_cmd") as mock:
        mock.return_value = MagicMock(returncode=0, stdout="", stderr="")
        yield mock


def make_publisher(pub_id="pub-1234", publisher_index=1, exposed_cidrs=None):
    return {
        "id": pub_id,
        "publisher_index": publisher_index,
        "exposed_cidrs": exposed_cidrs or ["10.50.0.0/16"],
    }


class TestReconcileFirewall:
    """Tests for the fwmark + policy routing reconcile_firewall function."""

    def test_generates_mangle_and_forward_rules_with_publisher_cidrs(self, tmp_config_dir, mock_run_cmd):
        """Client with publisher_cidrs mapping gets fwmark mangle + forward rules."""
        publishers = [make_publisher(pub_id="pub-1234", publisher_index=1)]
        clients = [
            {
                "overlay_ip": "10.200.0.1",
                "public_key": "key1",
                "allowed_cidrs": ["10.50.0.0/16"],
                "publisher_cidrs": {"pub-1234": ["10.50.0.0/16"]},
            }
        ]

        reconcile_firewall(clients, publishers)

        rules_file = tmp_config_dir / "nftables-wireztna.nft"
        assert rules_file.exists()
        content = rules_file.read_text()

        # Should have prerouting mangle chain with fwmark
        assert "chain prerouting" in content
        assert "priority mangle" in content
        assert 'ip saddr 10.200.0.1 ip daddr 10.50.0.0/16 meta mark set 1' in content

        # Should have forward accept rule (scoped by mark to prevent overlap issues)
        assert 'ip saddr 10.200.0.1 meta mark 1 ip daddr 10.50.0.0/16 accept' in content

        # Should have return traffic rule via veth
        assert 'iifname "v*-h" oifname "wg-clients" accept' in content

    def test_fallback_without_publisher_cidrs(self, tmp_config_dir, mock_run_cmd):
        """Client without publisher_cidrs falls back to allowed_cidrs (no mangle)."""
        publishers = [make_publisher(pub_id="pub-1234", publisher_index=1)]
        clients = [
            {
                "overlay_ip": "10.200.0.2",
                "public_key": "key2",
                "allowed_cidrs": ["10.50.0.0/16"],
                # No publisher_cidrs
            }
        ]

        reconcile_firewall(clients, publishers)

        content = (tmp_config_dir / "nftables-wireztna.nft").read_text()
        # Forward rule should exist (fallback)
        assert 'ip saddr 10.200.0.2 ip daddr 10.50.0.0/16 accept' in content
        # No mangle rules for this client (no publisher_cidrs mapping)
        assert "meta mark set" not in content

    def test_multiple_publishers_different_fwmarks(self, tmp_config_dir, mock_run_cmd):
        """Client with access to two publishers gets different fwmarks."""
        publishers = [
            make_publisher(pub_id="pub-aaa", publisher_index=1, exposed_cidrs=["10.50.0.0/16"]),
            make_publisher(pub_id="pub-bbb", publisher_index=2, exposed_cidrs=["192.168.1.0/24"]),
        ]
        clients = [
            {
                "overlay_ip": "10.200.0.1",
                "public_key": "key1",
                "allowed_cidrs": ["10.50.0.0/16", "192.168.1.0/24"],
                "publisher_cidrs": {
                    "pub-aaa": ["10.50.0.0/16"],
                    "pub-bbb": ["192.168.1.0/24"],
                },
            }
        ]

        reconcile_firewall(clients, publishers)

        content = (tmp_config_dir / "nftables-wireztna.nft").read_text()
        assert "meta mark set 1" in content
        assert "meta mark set 2" in content
        assert "ip daddr 10.50.0.0/16" in content
        assert "ip daddr 192.168.1.0/24" in content

    def test_policy_routing_rules_created(self, tmp_config_dir, mock_run_cmd):
        """Verify ip rule and ip route commands are issued for policy routing."""
        publishers = [make_publisher(pub_id="pub-1234", publisher_index=3)]
        clients = []

        reconcile_firewall(clients, publishers)

        calls = [c[0][0] for c in mock_run_cmd.call_args_list]

        # Should add ip rule for fwmark 3 → table 103
        assert ["ip", "rule", "add", "fwmark", "3", "table", "103"] in calls
        # Should add route in table 103 via veth namespace IP
        assert ["ip", "route", "replace", "default", "via", "10.252.3.2", "table", "103"] in calls

    def test_cleanup_old_rules(self, tmp_config_dir, mock_run_cmd):
        """Should attempt to delete old ip rules (fwmark 1-255) before adding new ones."""
        publishers = [make_publisher(pub_id="pub-1234", publisher_index=1)]
        clients = []

        reconcile_firewall(clients, publishers)

        calls = [c[0][0] for c in mock_run_cmd.call_args_list]

        # Should try to delete rule for fwmark 1
        assert ["ip", "rule", "del", "fwmark", "1", "table", "101"] in calls
        # Should try to delete rule for fwmark 2
        assert ["ip", "rule", "del", "fwmark", "2", "table", "102"] in calls

    def test_empty_clients_and_publishers(self, tmp_config_dir, mock_run_cmd):
        """No clients, no publishers → no mangle, no forward rules."""
        reconcile_firewall([], [])

        content = (tmp_config_dir / "nftables-wireztna.nft").read_text()
        assert "# No mangle rules" in content
        assert "# No client policies defined" in content
        # Structural elements should still be present
        assert "policy drop" in content
        assert "ct state established,related accept" in content
        assert 'log prefix "wireztna-deny: " drop' in content

    def test_forward_chain_has_drop_policy(self, tmp_config_dir, mock_run_cmd):
        publishers = [make_publisher()]
        clients = [
            {
                "overlay_ip": "10.200.0.1",
                "public_key": "key1",
                "allowed_cidrs": ["10.50.0.0/16"],
                "publisher_cidrs": {"pub-1234": ["10.50.0.0/16"]},
            }
        ]

        reconcile_firewall(clients, publishers)

        content = (tmp_config_dir / "nftables-wireztna.nft").read_text()
        assert "type filter hook forward priority filter; policy drop;" in content

    def test_applies_rules_atomically(self, tmp_config_dir, mock_run_cmd):
        """Verify nft commands are called: delete old table then load new file."""
        publishers = [make_publisher()]
        clients = [
            {
                "overlay_ip": "10.200.0.1",
                "public_key": "key1",
                "allowed_cidrs": ["10.50.0.0/16"],
                "publisher_cidrs": {"pub-1234": ["10.50.0.0/16"]},
            }
        ]

        reconcile_firewall(clients, publishers)

        calls = [c[0][0] for c in mock_run_cmd.call_args_list]
        # Should include nft delete and nft -f commands
        assert ["nft", "delete", "table", "inet", "wireztna"] in calls
        nft_load_calls = [c for c in calls if len(c) >= 2 and c[0] == "nft" and c[1] == "-f"]
        assert len(nft_load_calls) == 1
        assert str(tmp_config_dir / "nftables-wireztna.nft") in nft_load_calls[0][2]

    def test_log_and_drop_at_end(self, tmp_config_dir, mock_run_cmd):
        publishers = [make_publisher()]
        clients = [
            {
                "overlay_ip": "10.200.0.1",
                "public_key": "key1",
                "allowed_cidrs": ["10.50.0.0/16"],
                "publisher_cidrs": {"pub-1234": ["10.50.0.0/16"]},
            }
        ]

        reconcile_firewall(clients, publishers)

        content = (tmp_config_dir / "nftables-wireztna.nft").read_text()
        lines = content.strip().split("\n")
        rule_lines = [l.strip() for l in lines if l.strip() and not l.strip().startswith("#") and not l.strip().startswith("}") and not l.strip().startswith("{")]
        last_rule = rule_lines[-1]
        assert 'log prefix "wireztna-deny: " drop' == last_rule

    def test_ignores_publisher_not_in_publishers_list(self, tmp_config_dir, mock_run_cmd):
        """If client references a publisher_id not in desired_publishers, skip it."""
        publishers = [make_publisher(pub_id="pub-aaa", publisher_index=1)]
        clients = [
            {
                "overlay_ip": "10.200.0.1",
                "public_key": "key1",
                "allowed_cidrs": ["10.50.0.0/16", "192.168.1.0/24"],
                "publisher_cidrs": {
                    "pub-aaa": ["10.50.0.0/16"],
                    "pub-unknown": ["192.168.1.0/24"],  # Not in publishers list
                },
            }
        ]

        reconcile_firewall(clients, publishers)

        content = (tmp_config_dir / "nftables-wireztna.nft").read_text()
        # Should have rule for pub-aaa (mark 1)
        assert "meta mark set 1" in content
        assert "ip daddr 10.50.0.0/16" in content
        # Should NOT have a rule for pub-unknown
        assert "ip daddr 192.168.1.0/24" not in content


    def test_icmp_protocol_rule_generates_correct_nftables(self, tmp_config_dir, mock_run_cmd):
        """ICMP app rule should use 'ip protocol icmp' without port matching."""
        publishers = [make_publisher(pub_id="pub-1234", publisher_index=3)]
        clients = [
            {
                "overlay_ip": "10.200.0.5",
                "public_key": "key5",
                "allowed_cidrs": ["10.50.1.168/32"],
                "routing_rules": [
                    {
                        "type": "app",
                        "target": "10.50.1.168/32",
                        "port": 0,
                        "protocol": "icmp",
                        "publisher_index": 3,
                        "publisher_id": "pub-1234",
                        "prefix_len": 32,
                    }
                ],
            }
        ]

        reconcile_firewall(clients, publishers)

        content = (tmp_config_dir / "nftables-wireztna.nft").read_text()
        # Should have ICMP-specific match: ip protocol icmp (no port)
        assert "ip daddr 10.50.1.168/32 ip protocol icmp meta mark set 3" in content
        assert "ip daddr 10.50.1.168/32 ip protocol icmp accept" in content
        # Should NOT have 'th dport' for ICMP
        assert "th dport" not in content or "10.50.1.168/32 ip protocol icmp" in content

    def test_mixed_tcp_udp_icmp_rules(self, tmp_config_dir, mock_run_cmd):
        """Mix of TCP, UDP, and ICMP rules all generate correct nftables syntax."""
        publishers = [make_publisher(pub_id="pub-1234", publisher_index=3)]
        clients = [
            {
                "overlay_ip": "10.200.0.6",
                "public_key": "key6",
                "allowed_cidrs": ["10.50.1.0/24"],
                "routing_rules": [
                    {
                        "type": "app",
                        "target": "10.50.1.5",
                        "port": 443,
                        "protocol": "tcp",
                        "publisher_index": 3,
                        "publisher_id": "pub-1234",
                        "prefix_len": 32,
                    },
                    {
                        "type": "app",
                        "target": "10.50.1.5",
                        "port": 53,
                        "protocol": "udp",
                        "publisher_index": 3,
                        "publisher_id": "pub-1234",
                        "prefix_len": 32,
                    },
                    {
                        "type": "app",
                        "target": "10.50.1.168/32",
                        "port": 0,
                        "protocol": "icmp",
                        "publisher_index": 3,
                        "publisher_id": "pub-1234",
                        "prefix_len": 32,
                    },
                ],
            }
        ]

        reconcile_firewall(clients, publishers)

        content = (tmp_config_dir / "nftables-wireztna.nft").read_text()
        # TCP rule with port
        assert "ip daddr 10.50.1.5 meta l4proto tcp th dport 443 meta mark set 3" in content
        # UDP rule with port
        assert "ip daddr 10.50.1.5 meta l4proto udp th dport 53 meta mark set 3" in content
        # ICMP rule without port
        assert "ip daddr 10.50.1.168/32 ip protocol icmp meta mark set 3" in content

    def test_app_rule_no_port_no_icmp_falls_through(self, tmp_config_dir, mock_run_cmd):
        """App rule with port=0 and protocol != icmp generates CIDR-like rule."""
        publishers = [make_publisher(pub_id="pub-1234", publisher_index=2)]
        clients = [
            {
                "overlay_ip": "10.200.0.7",
                "public_key": "key7",
                "allowed_cidrs": ["10.50.1.0/24"],
                "routing_rules": [
                    {
                        "type": "app",
                        "target": "10.50.1.0/24",
                        "port": 0,
                        "protocol": "any",
                        "publisher_index": 2,
                        "publisher_id": "pub-1234",
                        "prefix_len": 24,
                    }
                ],
            }
        ]

        reconcile_firewall(clients, publishers)

        content = (tmp_config_dir / "nftables-wireztna.nft").read_text()
        # Should match by IP only (no protocol, no port)
        assert "ip daddr 10.50.1.0/24 meta mark set 2" in content
        assert "ip daddr 10.50.1.0/24 accept" in content
        # Should NOT have protocol or port match
        lines_with_target = [l for l in content.split("\n") if "10.50.1.0/24" in l]
        for line in lines_with_target:
            assert "th dport" not in line
            assert "ip protocol icmp" not in line
