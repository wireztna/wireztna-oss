"""
Seed script: Create announcement for TUI v0.9.12 release.

Run from broker:
  cd /opt/wireztna/control-plane
  python3 -m migrations.seed_tui_announcement
"""

import sqlite3
import uuid
import os
from datetime import datetime

DB_PATH = os.environ.get("DB_PATH", "/opt/wireztna/data/wireztna.db")

ANNOUNCEMENT_TITLE = "Cliente TUI v0.9.12 disponible"

ANNOUNCEMENT_MESSAGE = """Nueva versión del cliente terminal (TUI) con mejoras importantes:

• Interfaz interactiva con 3 pestañas: Estado, Proyectos y Debug
• Selector de nodos de salida (modo VPN) integrado en la TUI
• Panel de Debug local: estado WireGuard, rutas, DNS dividido y log de conexión
• Selector de proyectos/grupos con detección de solapamiento de CIDRs
• Indicadores en tiempo real: handshake, transferencia (rx/tx), TTL de sesión
• Comunicación IPC con el servicio/helper (Windows, macOS, Linux)
• Renovación automática de sesión en segundo plano
• Teclas rápidas: c=conectar, d=desconectar, r=renovar, tab=cambiar vista, 0-9=seleccionar grupo, e=nodo salida, s=split tunnel

Actualiza tu cliente descargando la nueva versión desde la sección Descargas."""

ANNOUNCEMENT_TYPE = "update"


def main():
    if not os.path.exists(DB_PATH):
        print(f"[ERROR] Database not found at {DB_PATH}")
        print("  Set DB_PATH env var or run from the broker host.")
        return

    conn = sqlite3.connect(DB_PATH)
    cursor = conn.cursor()

    # Check if an announcement with similar title already exists
    cursor.execute(
        "SELECT id FROM system_announcements WHERE title = ?",
        (ANNOUNCEMENT_TITLE,),
    )
    existing = cursor.fetchone()
    if existing:
        print(f"[SKIP] Announcement already exists (id={existing[0]})")
        conn.close()
        return

    ann_id = str(uuid.uuid4())
    now = datetime.utcnow().isoformat()

    cursor.execute(
        """INSERT INTO system_announcements (id, title, message, type, active, created_by, created_at)
           VALUES (?, ?, ?, ?, ?, ?, ?)""",
        (ann_id, ANNOUNCEMENT_TITLE, ANNOUNCEMENT_MESSAGE, ANNOUNCEMENT_TYPE, 1, None, now),
    )
    conn.commit()
    conn.close()

    print(f"[OK] Announcement created: id={ann_id}")
    print(f"     Title: {ANNOUNCEMENT_TITLE}")
    print(f"     Type: {ANNOUNCEMENT_TYPE}")
    print(f"     Active: true")


if __name__ == "__main__":
    main()
