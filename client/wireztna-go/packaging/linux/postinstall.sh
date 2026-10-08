#!/bin/bash
# Post-install script for WireZTNA Linux package

# Set network capabilities so the service doesn't need full root
setcap 'cap_net_admin+ep cap_net_raw+ep' /usr/local/bin/wireztna 2>/dev/null || true

# Reload systemd and enable service
systemctl daemon-reload
systemctl enable wireztna.service
systemctl start wireztna.service

echo "WireZTNA service installed and started."
echo ""
echo "Next steps:"
echo "  1. wireztna enroll \"<enrollment-url>\""
echo "  2. wireztna login"
echo "  3. Click the WireZTNA tray icon → Connect"
