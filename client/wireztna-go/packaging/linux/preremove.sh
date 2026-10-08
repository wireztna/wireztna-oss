#!/bin/bash
# Pre-remove script for WireZTNA Linux package

# Stop and disable the service
systemctl stop wireztna.service 2>/dev/null || true
systemctl disable wireztna.service 2>/dev/null || true

# Remove the socket file
rm -f /var/run/wireztna.sock

echo "WireZTNA service stopped and removed."
echo "User config preserved at ~/.wireztna/"
