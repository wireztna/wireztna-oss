package com.wireztna.android.ui.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Email
import androidx.compose.material.icons.filled.Key
import androidx.compose.material.icons.filled.Visibility
import androidx.compose.material.icons.filled.VisibilityOff
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.hilt.navigation.compose.hiltViewModel
import com.wireztna.android.ui.viewmodel.LoginMode
import com.wireztna.android.ui.viewmodel.LoginViewModel
import com.wireztna.android.ui.viewmodel.OtpStep

@Composable
fun LoginScreen(
    onLoginSuccess: () -> Unit,
    viewModel: LoginViewModel = hiltViewModel(),
) {
    val uiState by viewModel.uiState.collectAsState()
    var passwordVisible by remember { mutableStateOf(false) }

    LaunchedEffect(uiState.success) {
        if (uiState.success) onLoginSuccess()
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(32.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.Center,
    ) {
        Text(
            text = "Sign In",
            style = MaterialTheme.typography.headlineMedium,
            color = MaterialTheme.colorScheme.onBackground,
        )

        Spacer(modifier = Modifier.height(8.dp))

        Text(
            text = when (uiState.mode) {
                LoginMode.OTP -> "We'll send a one-time code to your email"
                LoginMode.PASSWORD -> "Enter your username and password"
            },
            style = MaterialTheme.typography.bodyMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            textAlign = TextAlign.Center,
        )

        Spacer(modifier = Modifier.height(32.dp))

        when (uiState.mode) {
            LoginMode.OTP -> OtpLoginForm(
                uiState = uiState,
                onUsernameChange = viewModel::setUsername,
                onCodeChange = viewModel::setOtpCode,
                onSubmit = viewModel::submit,
            )
            LoginMode.PASSWORD -> PasswordLoginForm(
                uiState = uiState,
                onUsernameChange = viewModel::setUsername,
                onPasswordChange = viewModel::setPassword,
                onSubmit = viewModel::submit,
                passwordVisible = passwordVisible,
                onTogglePasswordVisibility = { passwordVisible = !passwordVisible },
            )
        }

        Spacer(modifier = Modifier.height(16.dp))

        // Error
        if (uiState.error != null) {
            Text(
                text = uiState.error!!,
                color = MaterialTheme.colorScheme.error,
                style = MaterialTheme.typography.bodySmall,
                modifier = Modifier.fillMaxWidth(),
            )
            Spacer(modifier = Modifier.height(8.dp))
        }

        // Submit button
        Button(
            onClick = viewModel::submit,
            enabled = !uiState.isLoading,
            modifier = Modifier.fillMaxWidth(),
        ) {
            if (uiState.isLoading) {
                CircularProgressIndicator(
                    modifier = Modifier.size(20.dp),
                    strokeWidth = 2.dp,
                    color = MaterialTheme.colorScheme.onPrimary,
                )
            } else {
                val label = when {
                    uiState.mode == LoginMode.PASSWORD -> "Sign In"
                    uiState.otpStep == OtpStep.ENTER_USERNAME -> "Send Code"
                    else -> "Verify"
                }
                Text(label)
            }
        }

        Spacer(modifier = Modifier.height(24.dp))

        // Mode switcher
        TextButton(onClick = {
            val newMode = if (uiState.mode == LoginMode.OTP) LoginMode.PASSWORD else LoginMode.OTP
            viewModel.switchMode(newMode)
        }) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(
                    imageVector = if (uiState.mode == LoginMode.OTP) Icons.Default.Key else Icons.Default.Email,
                    contentDescription = null,
                    modifier = Modifier.size(16.dp),
                )
                Spacer(modifier = Modifier.width(4.dp))
                Text(
                    text = if (uiState.mode == LoginMode.OTP) "Use password instead"
                    else "Use email code instead",
                    style = MaterialTheme.typography.bodySmall,
                )
            }
        }
    }
}

@Composable
private fun OtpLoginForm(
    uiState: com.wireztna.android.ui.viewmodel.LoginUiState,
    onUsernameChange: (String) -> Unit,
    onCodeChange: (String) -> Unit,
    onSubmit: () -> Unit,
) {
    when (uiState.otpStep) {
        OtpStep.ENTER_USERNAME -> {
            OutlinedTextField(
                value = uiState.username,
                onValueChange = onUsernameChange,
                label = { Text("Username") },
                modifier = Modifier.fillMaxWidth(),
                singleLine = true,
                enabled = !uiState.isLoading,
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Go),
                keyboardActions = KeyboardActions(onGo = { onSubmit() }),
            )
        }
        OtpStep.ENTER_CODE -> {
            // Show email hint
            if (uiState.emailHint != null) {
                Text(
                    text = "Code sent to ${uiState.emailHint}",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.primary,
                )
                Spacer(modifier = Modifier.height(16.dp))
            }

            OutlinedTextField(
                value = uiState.otpCode,
                onValueChange = onCodeChange,
                label = { Text("Verification Code") },
                placeholder = { Text("Enter 6-digit code") },
                modifier = Modifier.fillMaxWidth(),
                singleLine = true,
                enabled = !uiState.isLoading,
                keyboardOptions = KeyboardOptions(
                    keyboardType = KeyboardType.Number,
                    imeAction = ImeAction.Go,
                ),
                keyboardActions = KeyboardActions(onGo = { onSubmit() }),
            )
        }
    }
}

@Composable
private fun PasswordLoginForm(
    uiState: com.wireztna.android.ui.viewmodel.LoginUiState,
    onUsernameChange: (String) -> Unit,
    onPasswordChange: (String) -> Unit,
    onSubmit: () -> Unit,
    passwordVisible: Boolean,
    onTogglePasswordVisibility: () -> Unit,
) {
    OutlinedTextField(
        value = uiState.username,
        onValueChange = onUsernameChange,
        label = { Text("Username") },
        modifier = Modifier.fillMaxWidth(),
        singleLine = true,
        enabled = !uiState.isLoading,
    )

    Spacer(modifier = Modifier.height(12.dp))

    OutlinedTextField(
        value = uiState.password,
        onValueChange = onPasswordChange,
        label = { Text("Password") },
        modifier = Modifier.fillMaxWidth(),
        singleLine = true,
        enabled = !uiState.isLoading,
        visualTransformation = if (passwordVisible) VisualTransformation.None
        else PasswordVisualTransformation(),
        keyboardOptions = KeyboardOptions(
            keyboardType = KeyboardType.Password,
            imeAction = ImeAction.Go,
        ),
        keyboardActions = KeyboardActions(onGo = { onSubmit() }),
        trailingIcon = {
            IconButton(onClick = onTogglePasswordVisibility) {
                Icon(
                    imageVector = if (passwordVisible) Icons.Default.VisibilityOff
                    else Icons.Default.Visibility,
                    contentDescription = "Toggle password visibility",
                )
            }
        },
    )
}
