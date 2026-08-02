package com.anglesgirl.echui

import android.content.Context
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map
import okhttp3.OkHttpClient
import okhttp3.Request
import java.net.URL

private val Context.dataStore by preferencesDataStore(name = "ech_preferences")

class EchProxyManager(private val context: Context) {
    private val dataStore = context.dataStore
    private val httpClient = OkHttpClient()

    companion object {
        private val DOH_KEY = stringPreferencesKey("ech_doh")
        private val DOH2_KEY = stringPreferencesKey("ech_doh2")
        private val DOH3_KEY = stringPreferencesKey("ech_doh3")
        private val IP_KEY = stringPreferencesKey("ech_ip")
        private val CONFIG_DOMAIN_KEY = stringPreferencesKey("ech_config_domain")
        private val MANUAL_KEY = stringPreferencesKey("ech_manual_override")
        private val LAST_REMOTE_KEY = stringPreferencesKey("ech_last_remote")

        const val DEFAULT_DOH = "https://pieqllv9i7.cloudflare-gateway.com/dns-query"
        val DEFAULT_DOH_FALLBACKS = listOf(
            DEFAULT_DOH,
            "https://m2b4x7vw98.cloudflare-gateway.com/dns-query",
            "https://dz1598pphb.cloudflare-gateway.com/dns-query",
        )
        const val DEFAULT_CONFIG_DOMAIN = "ech-config.anglesgirl.eu.org"
    }

    // === DoH Management ===
    fun getDohFlow(): Flow<String> = dataStore.data.map { prefs ->
        prefs[DOH_KEY] ?: DEFAULT_DOH
    }

    suspend fun setDoh(doh: String, manual: Boolean = true) {
        dataStore.edit { prefs ->
            prefs[DOH_KEY] = doh
            if (manual) prefs[MANUAL_KEY] = "1"
        }
    }

    // === IP Management ===
    fun getCustomIPsFlow(): Flow<String> = dataStore.data.map { prefs ->
        prefs[IP_KEY] ?: ""
    }

    suspend fun setCustomIPs(ips: String, manual: Boolean = true) {
        dataStore.edit { prefs ->
            prefs[IP_KEY] = ips
            if (manual) prefs[MANUAL_KEY] = "1"
        }
    }

    // === Config Domain ===
    fun getConfigDomainFlow(): Flow<String> = dataStore.data.map { prefs ->
        prefs[CONFIG_DOMAIN_KEY] ?: DEFAULT_CONFIG_DOMAIN
    }

    suspend fun setConfigDomain(domain: String) {
        dataStore.edit { prefs ->
            prefs[CONFIG_DOMAIN_KEY] = domain
        }
    }

    // === Manual Override ===
    suspend fun hasManualOverride(): Boolean {
        return dataStore.data.map { prefs ->
            prefs[MANUAL_KEY] == "1"
        }.first()
    }

    suspend fun clearManualOverride() {
        dataStore.edit { prefs ->
            prefs.remove(MANUAL_KEY)
        }
    }

    // === Remote Config ===
    suspend fun fetchRemoteConfig(domain: String): RemoteConfig {
        val dohList = getDohCandidates()
        var lastError: Exception? = null

        for (doh in dohList) {
            try {
                val txt = fetchTxt(doh, domain)
                return parseRemoteConfig(txt)
            } catch (e: Exception) {
                lastError = e
            }
        }

        throw lastError ?: Exception("No DoH endpoint available")
    }

    private suspend fun fetchTxt(doh: String, name: String): String {
        val url = "$doh?name=$name&type=TXT"
        val request = Request.Builder().url(url).build()
        return httpClient.newCall(request).execute().use { response ->
            response.body?.string() ?: throw Exception("Empty response")
        }
    }

    private suspend fun getDohCandidates(): List<String> {
        val doh = dataStore.data.map { prefs ->
            prefs[DOH_KEY] ?: DEFAULT_DOH
        }.first()
        val doh2 = dataStore.data.map { prefs ->
            prefs[DOH2_KEY]
        }.first()
        val doh3 = dataStore.data.map { prefs ->
            prefs[DOH3_KEY]
        }.first()
        return listOfNotNull(doh, doh2, doh3) + DEFAULT_DOH_FALLBACKS
    }

    private fun parseRemoteConfig(txt: String): RemoteConfig {
        val config = RemoteConfig()
        txt.split("\n").forEach { line ->
            line.split(";").forEach { part ->
                val idx = part.indexOf("=")
                if (idx > 0) {
                    val key = part.substring(0, idx).trim().lowercase()
                    val value = part.substring(idx + 1).trim()
                    when (key) {
                        "doh" -> config.doh = value
                        "doh2" -> config.doh2 = value
                        "doh3" -> config.doh3 = value
                        "ip", "ips" -> config.ip = value
                        "tr", "translate" -> config.tr = value
                    }
                }
            }
        }
        return config
    }

    // === Validation ===
    fun isValidDoh(s: String?): Boolean {
        return !s.isNullOrEmpty() && s.matches(Regex("^https://[^\\s]+$", RegexOption.IGNORE_CASE))
    }

    fun isValidIPList(s: String?): Boolean {
        if (s.isNullOrEmpty()) return false
        return s.split(",").all { ip ->
            ip.trim().matches(Regex("^[0-9.]+$|^[0-9a-f:]+$", RegexOption.IGNORE_CASE))
        }
    }
}

data class RemoteConfig(
    var doh: String? = null,
    var doh2: String? = null,
    var doh3: String? = null,
    var ip: String? = null,
    var tr: String? = null,
)
