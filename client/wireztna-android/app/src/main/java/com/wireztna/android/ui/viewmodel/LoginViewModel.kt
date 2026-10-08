package com.wireztna.android.ui.viewmodel

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.wireztna.android.data.config.ConfigStore
import com.wireztna.android.data.repository.WireZtnaRepository
import dagger.hilt.android.lifecycle.HiltViewModel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import javax.inject.Inject

enum class LoginMode {
    OTP,      // Default: email code
    PASSWORD, // Legacy: username/password
}

enum class OtpStep {
    ENTER_USERNAME, // Step 1: enter username, request code
    ENTER_CODE,     // Step 2: enter code received by email
}

data class LoginUiState(
    val username: String = "",
    val password: String = "",
    val otpCode: String = "",
    val mode: LoginMode = LoginMode.OTP,
    val otpStep: OtpStep = OtpStep.ENTER_USERNAME,
    val emailHint: String? = null,
    val isLoading: Boolean = false,
    val error: String? = null,
    val success: Boolean = false,
)

@HiltViewModel
class LoginViewModel @Inject constructor(
    private val repository: WireZtnaRepository,
    private val configStore: ConfigStore,
) : ViewModel() {

    private val _uiState = MutableStateFlow(
        LoginUiState(username = configStore.getUsername() ?: "")
    )
    val uiState: StateFlow<LoginUiState> = _uiState.asStateFlow()

    fun setUsername(value: String) {
        _uiState.value = _uiState.value.copy(username = value, error = null)
    }

    fun setPassword(value: String) {
        _uiState.value = _uiState.value.copy(password = value, error = null)
    }

    fun setOtpCode(value: String) {
        _uiState.value = _uiState.value.copy(otpCode = value, error = null)
    }

    fun switchMode(mode: LoginMode) {
        _uiState.value = _uiState.value.copy(
            mode = mode,
            error = null,
            otpStep = OtpStep.ENTER_USERNAME,
            otpCode = "",
        )
    }

    /**
     * Primary action button. Behavior depends on current mode and step.
     */
    fun submit() {
        val state = _uiState.value
        when (state.mode) {
            LoginMode.OTP -> {
                when (state.otpStep) {
                    OtpStep.ENTER_USERNAME -> requestOtpCode()
                    OtpStep.ENTER_CODE -> verifyOtpCode()
                }
            }
            LoginMode.PASSWORD -> loginWithPassword()
        }
    }

    private fun requestOtpCode() {
        val username = _uiState.value.username.trim()
        if (username.isBlank()) {
            _uiState.value = _uiState.value.copy(error = "Enter your username")
            return
        }

        viewModelScope.launch {
            _uiState.value = _uiState.value.copy(isLoading = true, error = null)
            val result = repository.otpRequest(username)
            _uiState.value = result.fold(
                onSuccess = { resp ->
                    _uiState.value.copy(
                        isLoading = false,
                        otpStep = OtpStep.ENTER_CODE,
                        emailHint = resp.emailHint,
                    )
                },
                onFailure = { e ->
                    _uiState.value.copy(
                        isLoading = false,
                        error = e.message ?: "Failed to request code",
                    )
                },
            )
        }
    }

    private fun verifyOtpCode() {
        val state = _uiState.value
        val code = state.otpCode.trim()
        if (code.isBlank()) {
            _uiState.value = state.copy(error = "Enter the code from your email")
            return
        }

        viewModelScope.launch {
            _uiState.value = _uiState.value.copy(isLoading = true, error = null)
            val result = repository.otpVerify(state.username.trim(), code)
            _uiState.value = result.fold(
                onSuccess = { _uiState.value.copy(isLoading = false, success = true) },
                onFailure = { e ->
                    _uiState.value.copy(
                        isLoading = false,
                        error = e.message ?: "Verification failed",
                    )
                },
            )
        }
    }

    private fun loginWithPassword() {
        val state = _uiState.value
        if (state.username.isBlank() || state.password.isBlank()) {
            _uiState.value = state.copy(error = "Username and password are required")
            return
        }

        viewModelScope.launch {
            _uiState.value = _uiState.value.copy(isLoading = true, error = null)
            val result = repository.login(state.username.trim(), state.password)
            _uiState.value = result.fold(
                onSuccess = { _uiState.value.copy(isLoading = false, success = true) },
                onFailure = { e ->
                    _uiState.value.copy(isLoading = false, error = e.message ?: "Login failed")
                },
            )
        }
    }
}
