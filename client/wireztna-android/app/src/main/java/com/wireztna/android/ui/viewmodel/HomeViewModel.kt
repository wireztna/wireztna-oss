package com.wireztna.android.ui.viewmodel

import android.app.Application
import android.content.Intent
import android.net.VpnService
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.wireguard.android.backend.GoBackend
import com.wireztna.android.data.api.AvailableGroupsResponse
import com.wireztna.android.data.config.ConfigStore
import com.wireztna.android.data.config.SecureKeyStore
import com.wireztna.android.data.repository.WireZtnaRepository
import com.wireztna.android.tunnel.TunnelManager
import com.wireztna.android.tunnel.TunnelState
import com.wireztna.android.worker.SessionRenewalWorker
import dagger.hilt.android.lifecycle.HiltViewModel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import javax.inject.Inject

data class HomeUiState(
    val isConnected: Boolean = false,
    val isConnecting: Boolean = false,
    val overlayIp: String = "",
    val username: String = "",
    val groupName: String? = null,
    val groups: AvailableGroupsResponse? = null,
    val selectedGroupId: String? = null,
    val selectedExitNodeId: String? = null,
    val selectedExitNodeName: String? = null,
    val error: String? = null,
)

@HiltViewModel
class HomeViewModel @Inject constructor(
    private val app: Application,
    private val repository: WireZtnaRepository,
    private val configStore: ConfigStore,
    private val tunnelManager: TunnelManager,
) : AndroidViewModel(app) {

    private val _uiState = MutableStateFlow(
        HomeUiState(
            overlayIp = configStore.getOverlayIp() ?: "",
            username = configStore.getUsername() ?: "",
            groupName = configStore.getSelectedGroupName(),
            selectedGroupId = configStore.getSelectedGroupId(),
        )
    )
    val uiState: StateFlow<HomeUiState> = _uiState.asStateFlow()

    init {
        // Observe tunnel state
        viewModelScope.launch {
            TunnelState.connected.collect { connected ->
                _uiState.value = _uiState.value.copy(isConnected = connected, isConnecting = false)
            }
        }
        viewModelScope.launch {
            TunnelState.error.collect { error ->
                if (error != null) {
                    _uiState.value = _uiState.value.copy(error = error, isConnecting = false)
                }
            }
        }
        // Load groups
        loadGroups()
    }

    fun connect(groupId: String? = null) {
        viewModelScope.launch {
            _uiState.value = _uiState.value.copy(isConnecting = true, error = null)

            // Renew session first to get fresh PSK
            val renewResult = repository.renewSession(
                groupId = groupId ?: _uiState.value.selectedGroupId,
                exitNodeId = _uiState.value.selectedExitNodeId,
            )
            if (renewResult.isFailure) {
                _uiState.value = _uiState.value.copy(
                    isConnecting = false,
                    error = renewResult.exceptionOrNull()?.message ?: "Session renew failed",
                )
                return@launch
            }

            // Connect tunnel
            try {
                tunnelManager.connect()
                // Enqueue periodic renewal
                SessionRenewalWorker.enqueue(app)
                _uiState.value = _uiState.value.copy(
                    selectedGroupId = groupId,
                    groupName = configStore.getSelectedGroupName(),
                )
            } catch (e: Exception) {
                _uiState.value = _uiState.value.copy(
                    isConnecting = false,
                    error = "Tunnel error: ${e.message}",
                )
            }
        }
    }

    fun disconnect() {
        viewModelScope.launch {
            tunnelManager.disconnect()
            SessionRenewalWorker.cancel(app)
        }
    }

    fun selectGroup(groupId: String?) {
        _uiState.value = _uiState.value.copy(selectedGroupId = groupId, selectedExitNodeId = null, selectedExitNodeName = null)
    }

    fun selectExitNode(exitNodeId: String?, exitNodeName: String?) {
        _uiState.value = _uiState.value.copy(
            selectedExitNodeId = exitNodeId,
            selectedExitNodeName = exitNodeName,
        )
    }

    fun logout() {
        viewModelScope.launch {
            tunnelManager.disconnect()
        }
        repository.logout()
    }

    fun unenroll() {
        viewModelScope.launch {
            tunnelManager.disconnect()
        }
        repository.unenroll()
    }

    private fun loadGroups() {
        viewModelScope.launch {
            val result = repository.getAvailableGroups()
            result.onSuccess { groups ->
                _uiState.value = _uiState.value.copy(groups = groups)
            }
        }
    }

    /**
     * Check if VPN permission is granted. Returns the intent to request it,
     * or null if already granted.
     */
    fun getVpnPermissionIntent(): Intent? {
        return GoBackend.VpnService.prepare(app)
    }
}
