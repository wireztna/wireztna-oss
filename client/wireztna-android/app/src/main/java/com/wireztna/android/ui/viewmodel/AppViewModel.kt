package com.wireztna.android.ui.viewmodel

import androidx.lifecycle.ViewModel
import com.wireztna.android.data.config.ConfigStore
import dagger.hilt.android.lifecycle.HiltViewModel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import javax.inject.Inject

data class AppUiState(
    val isEnrolled: Boolean = false,
    val isLoggedIn: Boolean = false,
)

@HiltViewModel
class AppViewModel @Inject constructor(
    private val configStore: ConfigStore,
) : ViewModel() {

    private val _uiState = MutableStateFlow(
        AppUiState(
            isEnrolled = configStore.isEnrolled(),
            isLoggedIn = configStore.isLoggedIn(),
        )
    )
    val uiState: StateFlow<AppUiState> = _uiState.asStateFlow()

    fun refresh() {
        _uiState.value = AppUiState(
            isEnrolled = configStore.isEnrolled(),
            isLoggedIn = configStore.isLoggedIn(),
        )
    }
}
