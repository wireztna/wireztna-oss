package com.wireztna.android.ui.viewmodel

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.wireztna.android.data.repository.WireZtnaRepository
import dagger.hilt.android.lifecycle.HiltViewModel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import javax.inject.Inject

data class EnrollUiState(
    val enrollUrl: String = "",
    val isLoading: Boolean = false,
    val error: String? = null,
    val success: Boolean = false,
    val username: String? = null,
)

@HiltViewModel
class EnrollViewModel @Inject constructor(
    private val repository: WireZtnaRepository,
) : ViewModel() {

    private val _uiState = MutableStateFlow(EnrollUiState())
    val uiState: StateFlow<EnrollUiState> = _uiState.asStateFlow()

    fun setEnrollUrl(url: String) {
        _uiState.value = _uiState.value.copy(enrollUrl = url, error = null)
    }

    fun enroll() {
        val url = _uiState.value.enrollUrl.trim()
        if (url.isBlank()) {
            _uiState.value = _uiState.value.copy(error = "Enter an enrollment URL or token")
            return
        }

        viewModelScope.launch {
            _uiState.value = _uiState.value.copy(isLoading = true, error = null)
            val result = repository.enroll(url)
            _uiState.value = result.fold(
                onSuccess = { username ->
                    _uiState.value.copy(
                        isLoading = false,
                        success = true,
                        username = username,
                    )
                },
                onFailure = { e ->
                    _uiState.value.copy(
                        isLoading = false,
                        error = "[${e.javaClass.simpleName}] ${e.message}",
                    )
                },
            )
        }
    }

    /**
     * Called when QR scanner decodes a URL.
     */
    fun enrollWithScannedUrl(url: String) {
        _uiState.value = _uiState.value.copy(enrollUrl = url)
        enroll()
    }
}
