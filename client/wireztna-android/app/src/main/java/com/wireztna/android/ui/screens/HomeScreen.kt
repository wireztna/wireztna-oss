package com.wireztna.android.ui.screens

import android.app.Activity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
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
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Circle
import androidx.compose.material.icons.filled.Logout
import androidx.compose.material.icons.filled.PowerSettingsNew
import androidx.compose.material.icons.filled.Shield
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ExposedDropdownMenuBox
import androidx.compose.material3.ExposedDropdownMenuDefaults
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.hilt.navigation.compose.hiltViewModel
import com.wireztna.android.ui.viewmodel.HomeViewModel

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun HomeScreen(
    onLogout: () -> Unit,
    onUnenroll: () -> Unit,
    viewModel: HomeViewModel = hiltViewModel(),
) {
    val uiState by viewModel.uiState.collectAsState()
    val context = LocalContext.current

    // VPN permission launcher
    val vpnPermissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { result ->
        if (result.resultCode == Activity.RESULT_OK) {
            viewModel.connect()
        }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(24.dp)
            .verticalScroll(rememberScrollState()),
    ) {
        // Header
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(
                    imageVector = Icons.Default.Shield,
                    contentDescription = null,
                    tint = MaterialTheme.colorScheme.primary,
                    modifier = Modifier.size(28.dp),
                )
                Spacer(modifier = Modifier.width(8.dp))
                Text("WireZTNA", style = MaterialTheme.typography.titleLarge)
            }

            IconButton(onClick = {
                viewModel.logout()
                onLogout()
            }) {
                Icon(Icons.Default.Logout, contentDescription = "Logout")
            }
        }

        Spacer(modifier = Modifier.height(24.dp))

        // Status card
        Card(
            modifier = Modifier.fillMaxWidth(),
            colors = CardDefaults.cardColors(
                containerColor = if (uiState.isConnected)
                    MaterialTheme.colorScheme.primaryContainer
                else MaterialTheme.colorScheme.surfaceVariant,
            ),
        ) {
            Column(modifier = Modifier.padding(20.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Icon(
                        imageVector = Icons.Default.Circle,
                        contentDescription = null,
                        tint = if (uiState.isConnected) Color(0xFF10B981) else Color.Gray,
                        modifier = Modifier.size(12.dp),
                    )
                    Spacer(modifier = Modifier.width(8.dp))
                    Text(
                        text = if (uiState.isConnected) "Connected" else "Disconnected",
                        style = MaterialTheme.typography.titleMedium,
                    )
                }

                Spacer(modifier = Modifier.height(12.dp))

                Text(
                    text = "User: ${uiState.username}",
                    style = MaterialTheme.typography.bodyMedium,
                )
                if (uiState.overlayIp.isNotBlank()) {
                    Text(
                        text = "Overlay IP: ${uiState.overlayIp}",
                        style = MaterialTheme.typography.bodyMedium,
                    )
                }
                if (uiState.groupName != null) {
                    Text(
                        text = "Project: ${uiState.groupName}",
                        style = MaterialTheme.typography.bodyMedium,
                    )
                }
                if (uiState.selectedExitNodeName != null) {
                    Text(
                        text = "Exit node: ${uiState.selectedExitNodeName}",
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.primary,
                    )
                }
            }
        }

        Spacer(modifier = Modifier.height(16.dp))

        // Error
        if (uiState.error != null) {
            Card(
                modifier = Modifier.fillMaxWidth(),
                colors = CardDefaults.cardColors(
                    containerColor = MaterialTheme.colorScheme.errorContainer,
                ),
            ) {
                Text(
                    text = uiState.error!!,
                    modifier = Modifier.padding(16.dp),
                    color = MaterialTheme.colorScheme.onErrorContainer,
                    style = MaterialTheme.typography.bodySmall,
                )
            }
            Spacer(modifier = Modifier.height(16.dp))
        }

        // Group selector (if overlap or VPN mode)
        val groups = uiState.groups
        if (groups != null && (groups.hasOverlap || groups.vpnMode || groups.exitNodes.isNotEmpty())) {
            var expanded by remember { mutableStateOf(false) }
            val selectedName = groups.groups.find { it.id == uiState.selectedGroupId }?.name
                ?: "All groups"

            ExposedDropdownMenuBox(
                expanded = expanded,
                onExpandedChange = { expanded = it },
            ) {
                OutlinedTextField(
                    value = selectedName,
                    onValueChange = {},
                    readOnly = true,
                    label = { Text("Project / Group") },
                    trailingIcon = { ExposedDropdownMenuDefaults.TrailingIcon(expanded) },
                    modifier = Modifier
                        .fillMaxWidth()
                        .menuAnchor(),
                )
                ExposedDropdownMenu(expanded = expanded, onDismissRequest = { expanded = false }) {
                    DropdownMenuItem(
                        text = { Text("All groups") },
                        onClick = {
                            viewModel.selectGroup(null)
                            expanded = false
                        },
                    )
                    groups.groups.forEach { group ->
                        DropdownMenuItem(
                            text = { Text("${group.name} (${group.cidrs.joinToString()})") },
                            onClick = {
                                viewModel.selectGroup(group.id)
                                expanded = false
                            },
                        )
                    }
                }
            }
            Spacer(modifier = Modifier.height(16.dp))
        }

        // Exit node selector (VPN mode)
        if (groups != null && groups.exitNodes.isNotEmpty()) {
            var exitExpanded by remember { mutableStateOf(false) }
            val selectedExit = uiState.selectedExitNodeName ?: "Split tunnel (no exit node)"

            Text(
                text = "VPN Mode",
                style = MaterialTheme.typography.labelMedium,
                color = MaterialTheme.colorScheme.primary,
                modifier = Modifier.padding(bottom = 4.dp),
            )

            ExposedDropdownMenuBox(
                expanded = exitExpanded,
                onExpandedChange = { exitExpanded = it },
            ) {
                OutlinedTextField(
                    value = selectedExit,
                    onValueChange = {},
                    readOnly = true,
                    label = { Text("Exit Node") },
                    trailingIcon = { ExposedDropdownMenuDefaults.TrailingIcon(exitExpanded) },
                    modifier = Modifier
                        .fillMaxWidth()
                        .menuAnchor(),
                )
                ExposedDropdownMenu(expanded = exitExpanded, onDismissRequest = { exitExpanded = false }) {
                    DropdownMenuItem(
                        text = { Text("Split tunnel (no exit node)") },
                        onClick = {
                            viewModel.selectExitNode(null, null)
                            exitExpanded = false
                        },
                    )
                    groups.exitNodes.forEach { node ->
                        val status = if (node.status == "online") "\u25CF" else "\u25CB"
                        val label = "$status ${node.name}${if (node.location.isNotBlank()) " — ${node.location}" else ""}"
                        DropdownMenuItem(
                            text = { Text(label) },
                            enabled = node.status == "online",
                            onClick = {
                                viewModel.selectExitNode(node.id, node.name)
                                exitExpanded = false
                            },
                        )
                    }
                }
            }
            Spacer(modifier = Modifier.height(16.dp))
        }

        // Connect / Disconnect button
        if (uiState.isConnected) {
            OutlinedButton(
                onClick = { viewModel.disconnect() },
                modifier = Modifier.fillMaxWidth(),
                colors = ButtonDefaults.outlinedButtonColors(
                    contentColor = MaterialTheme.colorScheme.error,
                ),
            ) {
                Icon(Icons.Default.PowerSettingsNew, contentDescription = null)
                Spacer(modifier = Modifier.width(8.dp))
                Text("Disconnect")
            }
        } else {
            Button(
                onClick = {
                    // Check VPN permission first
                    val vpnIntent = viewModel.getVpnPermissionIntent()
                    if (vpnIntent != null) {
                        vpnPermissionLauncher.launch(vpnIntent)
                    } else {
                        viewModel.connect()
                    }
                },
                enabled = !uiState.isConnecting,
                modifier = Modifier.fillMaxWidth(),
            ) {
                Icon(Icons.Default.PowerSettingsNew, contentDescription = null)
                Spacer(modifier = Modifier.width(8.dp))
                Text(if (uiState.isConnecting) "Connecting..." else "Connect")
            }
        }

        Spacer(modifier = Modifier.height(32.dp))

        // Unenroll option (destructive)
        FilledTonalButton(
            onClick = {
                viewModel.unenroll()
                onUnenroll()
            },
            modifier = Modifier.fillMaxWidth(),
            colors = ButtonDefaults.filledTonalButtonColors(
                containerColor = MaterialTheme.colorScheme.errorContainer,
                contentColor = MaterialTheme.colorScheme.onErrorContainer,
            ),
        ) {
            Text("Unenroll Device")
        }
    }
}
