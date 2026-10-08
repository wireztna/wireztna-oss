package com.wireztna.android.tunnel

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/**
 * Global observable tunnel state. Allows the UI to reactively show
 * connected/disconnected status without binding to the service.
 */
object TunnelState {

    private val _connected = MutableStateFlow(false)
    val connected: StateFlow<Boolean> = _connected.asStateFlow()

    private val _error = MutableStateFlow<String?>(null)
    val error: StateFlow<String?> = _error.asStateFlow()

    fun setConnected(value: Boolean) {
        _connected.value = value
        if (value) _error.value = null
    }

    fun setError(message: String) {
        _error.value = message
        _connected.value = false
    }

    fun clearError() {
        _error.value = null
    }
}
