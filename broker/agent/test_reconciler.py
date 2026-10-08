"""Unit tests for the reconciler module — publisher namespace reconciliation."""

import json
import subprocess
from pathlib import Path
from unittest.mock import MagicMock, patch, call

import pytest

# Patch httpx before importing reconciler
import sys
sys.modules.setdefault("httpx", MagicMock())

from broker.agent import reconciler
from broker.agent.reconciler import (
    get_existing_namespaces,
    load_namespace_map,
    save_namespace_map,
    reconcile_publishers,
    reconcile,
)


# ─── Helpers ───

def make_publisher(
    pub_id="a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    name="test-publisher",
    public_key="AbCdEfGhIjKlMnOpQrStUvWxYz123456789012345=",
    publisher_index=1,
    status="active",
    exposed_cidrs=None,
    virtual_cidr=None,
    endpoint="",
):
    if exposed_cidrs is None:
        exposed_cidrs = ["10.0.0.0/24"]
    if virtual_cidr is None:
        virtual_cidr = f"10.252.{publisher_index}.0/24"
    return {
        "id": pub_id,
        "name": name,
        "public_key": public_key,
        "publisher_index": publisher_index,
        "status": status,
        "exposed_cidrs": exposed_cidrs,
        "virtual_cidr": virtual_cidr,
        "endpoint": endpoint,
    }


def fake_run_cmd(cmd, check=False):
    """Simulates successful command execution."""
    return subprocess.CompletedProcess(args=cmd, returncode=0, stdout="", stderr="")


def fake_run_cmd_netns_empty(cmd, check=False):
    """Simulates 'ip netns list' returning empty."""
    if cmd == ["ip", "netns", "list"]:
        return subprocess.CompletedProcess(args=cmd, returncode=0, stdout="", stderr="")
    return subprocess.CompletedProcess(args=cmd, returncode=0, stdout="", stderr="")


def fake_run_cmd_netns_existing(existing_ns):
    """Returns a run_cmd that reports existing namespaces."""
    def _run(cmd, check=False):
        if cmd == ["ip", "netns", "list"]:
            output = "\n".join(existing_ns)
            return subprocess.CompletedProcess(args=cmd, returncode=0, stdout=output, stderr="")
        return subprocess.CompletedProcess(args=cmd, returncode=0, stdout="", stderr="")
    return _run


# ─── Tests: get_existing_namespaces ───

class TestGetExistingNamespaces:
    @patch("broker.agent.reconciler.run_cmd")
    def test_empty_output(self, mock_run):
        mock_run.return_value = subprocess.CompletedProcess(
            args=[], returncode=0, stdout="", stderr=""
        )
        assert get_existing_namespaces() == set()

    @patch("broker.agent.reconciler.run_cmd")
    def test_parses_ns_prefixed_names(self, mock_run):
        mock_run.return_value = subprocess.CompletedProcess(
            args=[], returncode=0,
            stdout="ns-a1b2c3d4 (id: 0)\nns-deadbeef (id: 1)\nsome-other-ns\n",
            stderr=""
        )
        result = get_existing_namespaces()
        assert result == {"ns-a1b2c3d4", "ns-deadbeef"}

    @patch("broker.agent.reconciler.run_cmd")
    def test_ignores_non_ns_prefixed(self, mock_run):
        mock_run.return_value = subprocess.CompletedProcess(
            args=[], returncode=0,
            stdout="global-ns\nns-12345678\n",
            stderr=""
        )
        result = get_existing_namespaces()
        assert result == {"ns-12345678"}


# ─── Tests: namespace map persistence ───

class TestNamespaceMapPersistence:
    def test_load_nonexistent_returns_empty(self, tmp_path, monkeypatch):
        monkeypatch.setattr(reconciler, "NAMESPACE_MAP_FILE", tmp_path / "nonexistent.json")
        assert load_namespace_map() == {}

    def test_save_and_load_roundtrip(self, tmp_path, monkeypatch):
        map_file = tmp_path / "namespace-map.json"
        monkeypatch.setattr(reconciler, "NAMESPACE_MAP_FILE", map_file)
        monkeypatch.setattr(reconciler, "CONFIG_DIR", tmp_path)

        test_map = {
            "ns-a1b2c3d4": {"publisher_id": "a1b2c3d4-full-uuid", "publisher_index": 1},
            "ns-deadbeef": {"publisher_id": "deadbeef-full-uuid", "publisher_index": 2},
        }
        save_namespace_map(test_map)
        loaded = load_namespace_map()
        assert loaded == test_map

    def test_load_corrupted_returns_empty(self, tmp_path, monkeypatch):
        map_file = tmp_path / "namespace-map.json"
        map_file.write_text("not valid json {{{")
        monkeypatch.setattr(reconciler, "NAMESPACE_MAP_FILE", map_file)
        assert load_namespace_map() == {}


# ─── Tests: reconcile_publishers ───

class TestReconcilePublishers:
    @patch("broker.agent.reconciler._regenerate_corefile")
    @patch("broker.agent.reconciler.run_cmd")
    def test_creates_namespace_for_new_publisher(self, mock_run, mock_corefile, tmp_path, monkeypatch):
        """New publisher with public_key and active status → creates namespace + WG."""
        monkeypatch.setattr(reconciler, "NAMESPACE_MAP_FILE", tmp_path / "ns-map.json")
        monkeypatch.setattr(reconciler, "CONFIG_DIR", tmp_path)
        monkeypatch.setattr(reconciler, "SCRIPTS_DIR", Path("/opt/wireztna/scripts"))

        mock_run.side_effect = fake_run_cmd_netns_existing([])

        pub = make_publisher()
        reconcile_publishers([pub])

        # Verify scripts were called
        calls = [c[0][0] for c in mock_run.call_args_list]

        # Should call create-publisher-ns.sh
        assert any("create-publisher-ns.sh" in str(c) for c in calls)
        # Should call create-ns-wg.sh
        assert any("create-ns-wg.sh" in str(c) for c in calls)
        # Should NOT call setup-ns-nat.sh (transparent routing, no NAT needed)
        assert not any("setup-ns-nat.sh" in str(c) for c in calls)

    @patch("broker.agent.reconciler._regenerate_corefile")
    @patch("broker.agent.reconciler.run_cmd")
    def test_skips_pending_publisher(self, mock_run, mock_corefile, tmp_path, monkeypatch):
        """Publisher with status='pending' should be skipped entirely."""
        monkeypatch.setattr(reconciler, "NAMESPACE_MAP_FILE", tmp_path / "ns-map.json")
        monkeypatch.setattr(reconciler, "CONFIG_DIR", tmp_path)

        mock_run.side_effect = fake_run_cmd_netns_existing([])

        pub = make_publisher(status="pending")
        reconcile_publishers([pub])

        # Should only call ip netns list (to get existing), no scripts
        calls = [c[0][0] for c in mock_run.call_args_list]
        assert not any("create-publisher-ns.sh" in str(c) for c in calls)

    @patch("broker.agent.reconciler._regenerate_corefile")
    @patch("broker.agent.reconciler.run_cmd")
    def test_skips_publisher_without_public_key(self, mock_run, mock_corefile, tmp_path, monkeypatch):
        """Publisher without a public_key should be skipped."""
        monkeypatch.setattr(reconciler, "NAMESPACE_MAP_FILE", tmp_path / "ns-map.json")
        monkeypatch.setattr(reconciler, "CONFIG_DIR", tmp_path)

        mock_run.side_effect = fake_run_cmd_netns_existing([])

        pub = make_publisher(public_key="")
        reconcile_publishers([pub])

        calls = [c[0][0] for c in mock_run.call_args_list]
        assert not any("create-publisher-ns.sh" in str(c) for c in calls)

    @patch("broker.agent.reconciler._regenerate_corefile")
    @patch("broker.agent.reconciler.run_cmd")
    def test_destroys_stale_namespace(self, mock_run, mock_corefile, tmp_path, monkeypatch):
        """Namespace exists but publisher no longer desired → destroy it."""
        map_file = tmp_path / "ns-map.json"
        # Pre-populate namespace map with a namespace that is no longer desired
        existing_map = {
            "ns-deadbeef": {"publisher_id": "deadbeef-1234-5678-abcd-ef1234567890", "publisher_index": 5}
        }
        map_file.write_text(json.dumps(existing_map))
        monkeypatch.setattr(reconciler, "NAMESPACE_MAP_FILE", map_file)
        monkeypatch.setattr(reconciler, "CONFIG_DIR", tmp_path)
        monkeypatch.setattr(reconciler, "SCRIPTS_DIR", Path("/opt/wireztna/scripts"))

        # System reports ns-deadbeef exists but no publishers are desired
        mock_run.side_effect = fake_run_cmd_netns_existing(["ns-deadbeef"])

        reconcile_publishers([])

        calls = [c[0][0] for c in mock_run.call_args_list]
        assert any("destroy-publisher-ns.sh" in str(c) for c in calls)

    @patch("broker.agent.reconciler._regenerate_corefile")
    @patch("broker.agent.reconciler.run_cmd")
    def test_does_not_recreate_existing_namespace(self, mock_run, mock_corefile, tmp_path, monkeypatch):
        """If namespace already exists and config unchanged, skip create-publisher-ns.sh AND WG/DNS setup."""
        map_file = tmp_path / "ns-map.json"
        monkeypatch.setattr(reconciler, "NAMESPACE_MAP_FILE", map_file)
        monkeypatch.setattr(reconciler, "CONFIG_DIR", tmp_path)
        monkeypatch.setattr(reconciler, "SCRIPTS_DIR", Path("/opt/wireztna/scripts"))

        pub = make_publisher()
        id_short = pub["id"][:8]
        ns_name = f"ns-{id_short}"

        # Pre-populate namespace map with matching config hash so reconciler sees no change
        from broker.agent.reconciler import _publisher_config_hash
        existing_map = {
            ns_name: {
                "publisher_id": pub["id"],
                "publisher_index": pub["publisher_index"],
                "config_hash": _publisher_config_hash(pub),
            }
        }
        map_file.write_text(json.dumps(existing_map))

        mock_run.side_effect = fake_run_cmd_netns_existing([ns_name])

        reconcile_publishers([pub])

        calls = [c[0][0] for c in mock_run.call_args_list]
        # Should NOT call create-publisher-ns.sh (namespace exists)
        assert not any("create-publisher-ns.sh" in str(c) for c in calls)
        # Should NOT call create-ns-wg.sh (config unchanged, don't kill handshake)
        assert not any("create-ns-wg.sh" in str(c) for c in calls)

    @patch("broker.agent.reconciler._regenerate_corefile")
    @patch("broker.agent.reconciler.run_cmd")
    def test_reapplies_wg_when_config_changes(self, mock_run, mock_corefile, tmp_path, monkeypatch):
        """If namespace exists but config hash differs, re-apply WG/DNS setup."""
        map_file = tmp_path / "ns-map.json"
        monkeypatch.setattr(reconciler, "NAMESPACE_MAP_FILE", map_file)
        monkeypatch.setattr(reconciler, "CONFIG_DIR", tmp_path)
        monkeypatch.setattr(reconciler, "SCRIPTS_DIR", Path("/opt/wireztna/scripts"))

        pub = make_publisher(exposed_cidrs=["10.50.0.0/16"])
        id_short = pub["id"][:8]
        ns_name = f"ns-{id_short}"

        # Pre-populate namespace map with a DIFFERENT config hash (simulating old config)
        existing_map = {
            ns_name: {
                "publisher_id": pub["id"],
                "publisher_index": pub["publisher_index"],
                "config_hash": "old_hash_1234",
            }
        }
        map_file.write_text(json.dumps(existing_map))

        mock_run.side_effect = fake_run_cmd_netns_existing([ns_name])

        reconcile_publishers([pub])

        calls = [c[0][0] for c in mock_run.call_args_list]
        # Should NOT call create-publisher-ns.sh (namespace already exists)
        assert not any("create-publisher-ns.sh" in str(c) for c in calls)
        # SHOULD call create-ns-wg.sh (config changed, re-apply WG)
        assert any("create-ns-wg.sh" in str(c) for c in calls)

    @patch("broker.agent.reconciler._regenerate_corefile")
    @patch("broker.agent.reconciler.run_cmd")
    def test_passes_endpoint_to_wg_script(self, mock_run, mock_corefile, tmp_path, monkeypatch):
        """Publisher endpoint should be passed as optional arg to create-ns-wg.sh."""
        monkeypatch.setattr(reconciler, "NAMESPACE_MAP_FILE", tmp_path / "ns-map.json")
        monkeypatch.setattr(reconciler, "CONFIG_DIR", tmp_path)
        monkeypatch.setattr(reconciler, "SCRIPTS_DIR", Path("/opt/wireztna/scripts"))

        mock_run.side_effect = fake_run_cmd_netns_existing([])

        pub = make_publisher(endpoint="203.0.113.5:51821")
        reconcile_publishers([pub])

        # Find the create-ns-wg.sh call
        wg_calls = [
            c[0][0] for c in mock_run.call_args_list
            if "create-ns-wg.sh" in str(c[0][0])
        ]
        assert len(wg_calls) == 1
        assert "203.0.113.5:51821" in wg_calls[0]

    @patch("broker.agent.reconciler._regenerate_corefile")
    @patch("broker.agent.reconciler.run_cmd")
    def test_namespace_map_persisted_after_reconcile(self, mock_run, mock_corefile, tmp_path, monkeypatch):
        """After reconciliation, namespace map should be written to disk with config_hash."""
        map_file = tmp_path / "ns-map.json"
        monkeypatch.setattr(reconciler, "NAMESPACE_MAP_FILE", map_file)
        monkeypatch.setattr(reconciler, "CONFIG_DIR", tmp_path)
        monkeypatch.setattr(reconciler, "SCRIPTS_DIR", Path("/opt/wireztna/scripts"))

        mock_run.side_effect = fake_run_cmd_netns_existing([])

        pub = make_publisher()
        reconcile_publishers([pub])

        # Verify map file exists and contains the publisher
        assert map_file.exists()
        saved = json.loads(map_file.read_text())
        ns_name = f"ns-{pub['id'][:8]}"
        assert ns_name in saved
        assert saved[ns_name]["publisher_id"] == pub["id"]
        assert saved[ns_name]["publisher_index"] == pub["publisher_index"]
        assert "config_hash" in saved[ns_name]
        assert len(saved[ns_name]["config_hash"]) == 12  # 12-char hex hash

    @patch("broker.agent.reconciler._regenerate_corefile")
    @patch("broker.agent.reconciler.run_cmd")
    def test_tunnel_ips_derived_from_index(self, mock_run, mock_corefile, tmp_path, monkeypatch):
        """Tunnel IPs should be 10.100.{index}.1 (broker) and 10.100.{index}.2 (publisher)."""
        monkeypatch.setattr(reconciler, "NAMESPACE_MAP_FILE", tmp_path / "ns-map.json")
        monkeypatch.setattr(reconciler, "CONFIG_DIR", tmp_path)
        monkeypatch.setattr(reconciler, "SCRIPTS_DIR", Path("/opt/wireztna/scripts"))

        mock_run.side_effect = fake_run_cmd_netns_existing([])

        pub = make_publisher(publisher_index=7)
        reconcile_publishers([pub])

        # Find the create-ns-wg.sh call args
        wg_calls = [
            c[0][0] for c in mock_run.call_args_list
            if "create-ns-wg.sh" in str(c[0][0])
        ]
        assert len(wg_calls) == 1
        assert "10.100.7.1" in wg_calls[0]
        assert "10.100.7.2" in wg_calls[0]
