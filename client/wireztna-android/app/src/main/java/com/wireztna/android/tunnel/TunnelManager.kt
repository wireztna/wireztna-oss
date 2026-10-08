package com.wireztna.android.tunnel

import android.content.Context
import com.wireguard.android.backend.GoBackend
import com.wireguard.android.backend.Tunnel
import com.wireguard.config.Config
import com.wireztna.android.data.config.ConfigStore
import com.wireztna.android.data.config.SecureKeyStore
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/**
 * Manages the WireGuard tunnel lifecycle using the wireguard-android GoBackend.
 *
 * The GoBackend internally:
 * - Creates a TUN fd via its own GoBackend.VpnService (declared in manifest)
 * - Runs wireguard-go userspace WireGuard
 * - Manages the interface lifecycle
 *
 * We just call setState(UP/DOWN) with a parsed Config.
 */
class TunnelManager(
    private val appContext: Context,
    private val configStore: ConfigStore,
    private val keyStore: SecureKeyStore,
) {
    private val backend: GoBackend by lazy { GoBackend(appContext) }
    private val tunnel: WireZtnaTunnel = WireZtnaTunnel("wireztna")

    /**
     * Connect the tunnel. Must be called from a coroutine (runs on IO dispatcher).
     * Throws on failure.
     */
    suspend fun connect() {
        val configStr = TunnelConfigBuilder.build(configStore, keyStore)
            ?: throw IllegalStateException("Missing tunnel configuration — re-enroll")

        val config = withContext(Dispatchers.IO) {
            Config.parse(configStr.reader().buffered())
        }

        withContext(Dispatchers.IO) {
            backend.setState(tunnel, Tunnel.State.UP, config)
        }
    }

    /**
     * Disconnect the tunnel.
     */
    suspend fun disconnect() {
        try {
            withContext(Dispatchers.IO) {
                backend.setState(tunnel, Tunnel.State.DOWN, null)
            }
        } catch (_: Exception) {
            // Best-effort
        }
        TunnelState.setConnected(false)
    }

    /**
     * Reconfigure with new PSK (after session renewal).
     */
    suspend fun reconfigure() {
        if (TunnelState.connected.value) {
            // Bring down, then up with new config
            disconnect()
            connect()
        }
    }

    fun isConnected(): Boolean = TunnelState.connected.value
}
