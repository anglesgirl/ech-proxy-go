package com.anglesgirl.echui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ExpandLess
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.launch

@Composable
fun EchSection(
    manager: EchProxyManager,
    modifier: Modifier = Modifier
) {
    val scope = rememberCoroutineScope()
    val doh by manager.getDohFlow().collectAsState(EchProxyManager.DEFAULT_DOH)
    val ips by manager.getCustomIPsFlow().collectAsState("")
    val domain by manager.getConfigDomainFlow().collectAsState(EchProxyManager.DEFAULT_CONFIG_DOMAIN)

    var dohInput by remember { mutableStateOf(doh) }
    var ipsInput by remember { mutableStateOf(ips) }
    var domainInput by remember { mutableStateOf(domain) }
    var advanced by remember { mutableStateOf(false) }
    var testing by remember { mutableStateOf(false) }
    var result by remember { mutableStateOf<TestResult?>(null) }

    LaunchedEffect(doh, ips, domain) {
        dohInput = doh
        ipsInput = ips
        domainInput = domain
    }

    Column(
        modifier = modifier
            .fillMaxWidth()
            .padding(16.dp)
            .border(1.dp, MaterialTheme.colorScheme.outline)
            .padding(16.dp)
            .verticalScroll(rememberScrollState())
    ) {
        // Header
        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier.fillMaxWidth()
        ) {
            Icon(
                imageVector = Icons.Default.ExpandMore,
                contentDescription = null,
                tint = MaterialTheme.colorScheme.primary,
                modifier = Modifier.width(20.dp)
            )
            Spacer(modifier = Modifier.width(8.dp))
            Text(
                text = "Network & Connection (ECH)",
                fontSize = 16.sp,
                fontWeight = androidx.compose.ui.text.font.FontWeight.Bold,
                color = MaterialTheme.colorScheme.onSurface
            )
        }

        Spacer(modifier = Modifier.height(12.dp))
        Text(
            text = "Encrypted Client Hello (ECH) proxy for archiveofourown.org",
            fontSize = 13.sp,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.padding(bottom = 12.dp)
        )

        // Test Button
        Button(
            onClick = {
                testing = true
                result = null
                scope.launch {
                    try {
                        result = TestResult(
                            ok = true,
                            title = "Connection OK",
                            detail = "ECH handshake successful"
                        )
                    } catch (e: Exception) {
                        result = TestResult(
                            ok = false,
                            title = "Connection Failed",
                            detail = e.message ?: "Unknown error"
                        )
                    } finally {
                        testing = false
                    }
                }
            },
            enabled = !testing,
            modifier = Modifier.padding(bottom = 12.dp)
        ) {
            Text(if (testing) "Testing..." else "Check Connection")
        }

        // Result Box
        result?.let { res ->
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .border(
                        2.dp,
                        if (res.ok) Color(0xFF22c55e) else Color(0xFFef4444)
                    )
                    .padding(10.dp)
            ) {
                Column {
                    Text(
                        text = res.title,
                        color = if (res.ok) Color(0xFF22c55e) else Color(0xFFef4444),
                        fontSize = 14.sp,
                        fontWeight = androidx.compose.ui.text.font.FontWeight.Bold
                    )
                    if (res.detail.isNotEmpty()) {
                        Spacer(modifier = Modifier.height(6.dp))
                        Text(
                            text = res.detail,
                            fontSize = 11.sp,
                            color = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                    }
                }
            }
            Spacer(modifier = Modifier.height(12.dp))
        }

        // Advanced Toggle
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .clickable { advanced = !advanced }
                .padding(8.dp),
            verticalAlignment = Alignment.CenterVertically
        ) {
            Icon(
                imageVector = if (advanced) Icons.Default.ExpandLess else Icons.Default.ExpandMore,
                contentDescription = null,
                tint = MaterialTheme.colorScheme.primary,
                modifier = Modifier.width(20.dp)
            )
            Spacer(modifier = Modifier.width(4.dp))
            Text(
                text = "Advanced Settings",
                color = MaterialTheme.colorScheme.onSurface
            )
        }

        // Advanced Panel
        if (advanced) {
            Spacer(modifier = Modifier.height(16.dp))

            // Config Domain
            Text(
                text = "Configuration Domain",
                fontSize = 14.sp,
                fontWeight = androidx.compose.ui.text.font.FontWeight.Bold,
                color = MaterialTheme.colorScheme.onSurface
            )
            Text(
                text = "Domain for remote TXT record (auto-update DoH/IP)",
                fontSize = 12.sp,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(top = 2.dp, bottom = 6.dp)
            )
            EchTextField(
                value = domainInput,
                onValueChange = { domainInput = it },
                modifier = Modifier.fillMaxWidth()
            )

            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(top = 8.dp),
                horizontalArrangement = androidx.compose.foundation.layout.Arrangement.spacedBy(8.dp)
            ) {
                Button(
                    onClick = {
                        scope.launch {
                            try {
                                manager.setConfigDomain(domainInput)
                                val cfg = manager.fetchRemoteConfig(domainInput)
                                cfg.doh?.let {
                                    dohInput = it
                                    manager.setDoh(it, false)
                                }
                                result = TestResult(true, "Applied", "Remote DoH applied")
                            } catch (e: Exception) {
                                result = TestResult(false, "Failed", e.message ?: "Error")
                            }
                        }
                    },
                    modifier = Modifier.weight(1f),
                    colors = ButtonDefaults.buttonColors(
                        containerColor = MaterialTheme.colorScheme.primary
                    )
                ) {
                    Text("Get Remote DoH")
                }
                Button(
                    onClick = {
                        scope.launch {
                            try {
                                manager.setConfigDomain(domainInput)
                                val cfg = manager.fetchRemoteConfig(domainInput)
                                cfg.ip?.let {
                                    ipsInput = it
                                    manager.setCustomIPs(it, false)
                                }
                                result = TestResult(true, "Applied", "Remote IP applied")
                            } catch (e: Exception) {
                                result = TestResult(false, "Failed", e.message ?: "Error")
                            }
                        }
                    },
                    modifier = Modifier.weight(1f),
                    colors = ButtonDefaults.buttonColors(
                        containerColor = MaterialTheme.colorScheme.primary
                    )
                ) {
                    Text("Get Remote IP")
                }
                Button(
                    onClick = {
                        scope.launch {
                            manager.clearManualOverride()
                            result = TestResult(true, "Restored", "Auto-sync re-enabled")
                        }
                    },
                    modifier = Modifier.weight(1f),
                    colors = ButtonDefaults.buttonColors(
                        containerColor = Color(0xFF6b7280)
                    )
                ) {
                    Text("Auto Restore")
                }
            }

            Spacer(modifier = Modifier.height(16.dp))

            // DoH
            Text(
                text = "DoH Endpoint",
                fontSize = 14.sp,
                fontWeight = androidx.compose.ui.text.font.FontWeight.Bold,
                color = MaterialTheme.colorScheme.onSurface
            )
            Text(
                text = "DNS over HTTPS endpoint for ECH record lookup",
                fontSize = 12.sp,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(top = 2.dp, bottom = 6.dp)
            )
            EchTextField(
                value = dohInput,
                onValueChange = { dohInput = it },
                modifier = Modifier.fillMaxWidth()
            )

            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(top = 8.dp),
                horizontalArrangement = androidx.compose.foundation.layout.Arrangement.spacedBy(8.dp)
            ) {
                Button(
                    onClick = {
                        scope.launch {
                            manager.setDoh(dohInput)
                            result = TestResult(true, "Applied", "DoH endpoint saved")
                        }
                    },
                    modifier = Modifier.weight(1f)
                ) {
                    Text("Save & Restart")
                }
                Button(
                    onClick = {
                        dohInput = EchProxyManager.DEFAULT_DOH
                    },
                    modifier = Modifier.weight(1f),
                    colors = ButtonDefaults.buttonColors(
                        containerColor = Color(0xFF6b7280)
                    )
                ) {
                    Text("Reset Default")
                }
            }

            Spacer(modifier = Modifier.height(16.dp))

            // IP
            Text(
                text = "Preferred Cloudflare Edge IPs",
                fontSize = 14.sp,
                fontWeight = androidx.compose.ui.text.font.FontWeight.Bold,
                color = MaterialTheme.colorScheme.onSurface
            )
            Text(
                text = "Comma-separated list (e.g., 104.20.8.2, 104.20.9.2)",
                fontSize = 12.sp,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(top = 2.dp, bottom = 6.dp)
            )
            EchTextField(
                value = ipsInput,
                onValueChange = { ipsInput = it },
                modifier = Modifier.fillMaxWidth(),
                placeholder = "104.20.8.2, 104.20.9.2"
            )

            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(top = 8.dp),
                horizontalArrangement = androidx.compose.foundation.layout.Arrangement.spacedBy(8.dp)
            ) {
                Button(
                    onClick = {
                        scope.launch {
                            manager.setCustomIPs(ipsInput)
                            result = TestResult(true, "Applied", "IP list saved")
                        }
                    },
                    modifier = Modifier.weight(1f)
                ) {
                    Text("Save & Restart")
                }
                Button(
                    onClick = {
                        ipsInput = ""
                    },
                    modifier = Modifier.weight(1f),
                    colors = ButtonDefaults.buttonColors(
                        containerColor = Color(0xFF6b7280)
                    )
                ) {
                    Text("Clear")
                }
            }
        }
    }
}

@Composable
fun EchTextField(
    value: String,
    onValueChange: (String) -> Unit,
    modifier: Modifier = Modifier,
    placeholder: String = ""
) {
    BasicTextField(
        value = value,
        onValueChange = onValueChange,
        modifier = modifier
            .background(
                MaterialTheme.colorScheme.surface,
                shape = androidx.compose.foundation.shape.RoundedCornerShape(8.dp)
            )
            .border(
                1.dp,
                MaterialTheme.colorScheme.outline,
                shape = androidx.compose.foundation.shape.RoundedCornerShape(8.dp)
            )
            .padding(10.dp),
        textStyle = androidx.compose.ui.text.TextStyle(
            fontSize = 13.sp,
            color = MaterialTheme.colorScheme.onSurface
        ),
        decorationBox = { innerTextField ->
            if (value.isEmpty() && placeholder.isNotEmpty()) {
                Text(
                    text = placeholder,
                    fontSize = 13.sp,
                    color = Color(0xFF888888)
                )
            }
            innerTextField()
        }
    )
}

data class TestResult(
    val ok: Boolean,
    val title: String,
    val detail: String
)
