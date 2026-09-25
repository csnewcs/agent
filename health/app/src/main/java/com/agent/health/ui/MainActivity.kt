package com.agent.health.ui

import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.provider.Settings
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.health.connect.client.PermissionController
import androidx.lifecycle.lifecycleScope
import com.agent.health.data.HealthConnectManager
import kotlinx.coroutines.launch

class MainActivity : ComponentActivity() {

    private lateinit var healthConnectManager: HealthConnectManager

    private val requestPermissionActivityContract =
        registerForActivityResult(PermissionController.createRequestPermissionResultContract()) { granted ->
            lifecycleScope.launch {
                if (healthConnectManager.hasAllPermissions()) {
                    Toast.makeText(this@MainActivity, "✅ 모든 Health Connect 권한 설정이 완료되었습니다.", Toast.LENGTH_SHORT).show()
                } else if (granted.isNotEmpty()) {
                    Toast.makeText(this@MainActivity, "선택하신 권한이 적용되었습니다. (남은 권한은 버튼을 눌러 추가 허용 가능)", Toast.LENGTH_LONG).show()
                } else {
                    Toast.makeText(this@MainActivity, "Health Connect 설정 화면으로 이동합니다.", Toast.LENGTH_SHORT).show()
                    openHealthConnectSettings()
                }
            }
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        healthConnectManager = HealthConnectManager(this)

        setContent {
            MainScreen(
                onRequestHealthPermissions = {
                    lifecycleScope.launch {
                        if (healthConnectManager.hasAllPermissions()) {
                            Toast.makeText(this@MainActivity, "✅ 모든 Health Connect 권한이 이미 허용되어 있습니다.", Toast.LENGTH_SHORT).show()
                        } else if (healthConnectManager.isHealthConnectAvailable()) {
                            try {
                                requestPermissionActivityContract.launch(healthConnectManager.PERMISSIONS)
                            } catch (e: Exception) {
                                openHealthConnectSettings()
                            }
                        } else {
                            Toast.makeText(
                                this@MainActivity,
                                "Health Connect를 지원하지 않거나 설치되어 있지 않습니다.",
                                Toast.LENGTH_LONG
                            ).show()
                        }
                    }
                }
            )
        }
    }

    private fun openHealthConnectSettings() {
        try {
            val intent = Intent("androidx.health.ACTION_HEALTH_CONNECT_SETTINGS")
            startActivity(intent)
        } catch (e: Exception) {
            try {
                val intent = Intent(Settings.ACTION_PRIVACY_SETTINGS)
                startActivity(intent)
            } catch (ex: Exception) {
                val intent = Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS).apply {
                    data = Uri.parse("package:$packageName")
                }
                startActivity(intent)
            }
        }
    }
}
