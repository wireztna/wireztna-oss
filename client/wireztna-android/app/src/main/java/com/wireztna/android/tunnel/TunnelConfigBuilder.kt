package com.wireztna.android.tunnel

import com.wireztna.android.data.config.ConfigStore
import com.wireztna.android.data.config.SecureKeyStore

/**
 * Builds a WireGuard config string from stored enrollment + session data.
 * The resulting string is the standard wg-quick format that the wireguard-android
 * tunnel library can parse.
 */
object TunnelConfigBuilder {

    /**
     * Generates a full WireGuard config file content from the config store state.
     * Returns null if essential data is missing.
     */
    fun build(configStore: ConfigStore, keyStore: SecureKeyStore): String? {
        val privateKey = keyStore.getPrivateKey()?.toBase64() ?: return null
        val overlayIp = configStore.getOverlayIp() ?: return null
        val brokerPublicKey = configStore.getBrokerPublicKey() ?: return null
        val brokerEndpoint = configStore.getBrokerEndpoint() ?: return null
        val dnsServer = configStore.getDnsServer() ?: "10.200.0.1"
        val psk = configStore.getSessionPsk()

        // Use session-provided AllowedIPs (they may differ from enrollment, e.g., exit node)
        val allowedIps = configStore.getSessionAllowedIps()
        if (allowedIps.isEmpty()) return null

        val sb = StringBuilder()

        // [Interface]
        sb.appendLine("[Interface]")
        sb.appendLine("PrivateKey = $privateKey")
        sb.appendLine("Address = $overlayIp/32")
        sb.appendLine("DNS = $dnsServer")
        sb.appendLine()

        // [Peer] — the broker
        sb.appendLine("[Peer]")
        sb.appendLine("PublicKey = $brokerPublicKey")
        if (psk != null) {
            sb.appendLine("PresharedKey = $psk")
        }
        sb.appendLine("Endpoint = $brokerEndpoint")
        sb.appendLine("AllowedIPs = ${allowedIps.joinToString(", ")}")
        sb.appendLine("PersistentKeepalive = 25")

        return sb.toString()
    }
}
