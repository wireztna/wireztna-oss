#!/bin/bash
# ─────────────────────────────────────────────────────────────────
# WireZTNA TUI Demo Recording Script
#
# This script records a polished asciinema demo of the TUI.
# Run it, then perform the actions manually (scripted guidance below).
#
# Usage:
#   ./record-tui.sh          # Records to demo/tui-demo.cast
#   ./record-tui.sh render   # Renders the last recording to GIF
# ─────────────────────────────────────────────────────────────────

CAST_FILE="$(dirname "$0")/tui-demo.cast"
GIF_FILE="$(dirname "$0")/tui-demo.gif"
COLS=110
ROWS=32

if [ "$1" = "render" ]; then
    echo "Rendering GIF from $CAST_FILE..."
    agg "$CAST_FILE" "$GIF_FILE" \
        --theme monokai \
        --font-size 14 \
        --speed 1.2 \
        --last-frame-duration 3
    echo "✓ GIF ready: $GIF_FILE ($(du -h "$GIF_FILE" | cut -f1))"
    echo ""
    echo "To optimize further: gifsicle -O3 --lossy=80 $GIF_FILE -o tui-demo-opt.gif"
    exit 0
fi

echo "╭─────────────────────────────────────────────────────────╮"
echo "│  WireZTNA TUI Demo Recording                            │"
echo "│                                                         │"
echo "│  The recording will start in a new shell.               │"
echo "│  Follow this script to showcase the TUI:                │"
echo "│                                                         │"
echo "│  1. Run: sudo wireztna tui                              │"
echo "│  2. Wait 2-3s on Status tab (show connection info)      │"
echo "│  3. Press TAB → Projects tab (show groups/cursor)       │"
echo "│  4. Press ↓↓↓ to move cursor, then Enter to select     │"
echo "│  5. Press TAB → Network tab (show WG/routes/DNS)        │"
echo "│  6. Press TAB → Logs tab (show live log stream)         │"
echo "│  7. Press 'f' to cycle filter (ALL→INFO→WARN→ERROR)     │"
echo "│  8. Press 'G' to jump to tail                           │"
echo "│  9. Press TAB → back to Status                          │"
echo "│  10. Press 'p' for ping test (show results)             │"
echo "│  11. Wait 2s, then press 'q' to quit                    │"
echo "│                                                         │"
echo "│  Tips:                                                  │"
echo "│  - Move slowly between actions (1-2s pauses)            │"
echo "│  - The GIF will be sped up 1.2x so normal pace is fine  │"
echo "│  - Press Ctrl+D or 'exit' when done to stop recording   │"
echo "╰─────────────────────────────────────────────────────────╯"
echo ""
echo "Starting recording in 3s..."
sleep 3

# Record with fixed terminal size for consistent output
asciinema rec "$CAST_FILE" \
    --cols "$COLS" \
    --rows "$ROWS" \
    --title "WireZTNA TUI — Zero Trust Client" \
    --idle-time-limit 3

echo ""
echo "✓ Recording saved to: $CAST_FILE"
echo ""
echo "Next steps:"
echo "  1. Preview:  asciinema play $CAST_FILE"
echo "  2. Render:   $0 render"
echo "  3. Upload:   asciinema upload $CAST_FILE  (optional, gets shareable URL)"
