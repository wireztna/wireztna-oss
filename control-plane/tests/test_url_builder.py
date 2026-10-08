"""Unit tests for the external URL builder (CE scheme/base-URL helper)."""

import pytest

from app.config import settings
from app.services.url_builder import external_base_url, external_ws_base_url


@pytest.fixture
def clean_url_settings(monkeypatch):
    """Reset the URL-related settings to known defaults for each test."""
    monkeypatch.setattr(settings, "broker_public_scheme", "http")
    monkeypatch.setattr(settings, "broker_public_endpoint", "")
    monkeypatch.setattr(settings, "public_base_url", "")
    return settings


def test_http_scheme_with_endpoint(clean_url_settings, monkeypatch):
    monkeypatch.setattr(settings, "broker_public_endpoint", "203.0.113.10")

    assert external_base_url() == "http://203.0.113.10"
    assert external_ws_base_url() == "ws://203.0.113.10"


def test_public_base_url_override_https(clean_url_settings, monkeypatch):
    # Even with a different endpoint/scheme, public_base_url wins.
    monkeypatch.setattr(settings, "broker_public_endpoint", "203.0.113.10")
    monkeypatch.setattr(settings, "public_base_url", "https://vpn.example.com")

    assert external_base_url() == "https://vpn.example.com"
    assert external_ws_base_url() == "wss://vpn.example.com"


def test_https_scheme_with_endpoint(clean_url_settings, monkeypatch):
    monkeypatch.setattr(settings, "broker_public_scheme", "https")
    monkeypatch.setattr(settings, "broker_public_endpoint", "203.0.113.10")

    assert external_base_url() == "https://203.0.113.10"
    assert external_ws_base_url() == "wss://203.0.113.10"


def test_dev_fallback_without_endpoint(clean_url_settings):
    # No endpoint, no override -> localhost dev fallback over http.
    assert external_base_url() == "http://127.0.0.1"
    assert external_ws_base_url() == "ws://127.0.0.1"
