package com.wireztna.android.tunnel

import com.wireguard.android.backend.Tunnel

/**
 * Tunnel name holder for the wireguard-android GoBackend.
 * The GoBackend has its own internal VpnService (GoBackend$VpnService).
 * We just need to provide a Tunnel interface implementation.
 */
class WireZtnaTunnel(private val name: String = "wireztna") : Tunnel {
    override fun getName(): String = name
    override fun onStateChange(newState: Tunnel.State) {
        TunnelState.setConnected(newState == Tunnel.State.UP)
    }
}
