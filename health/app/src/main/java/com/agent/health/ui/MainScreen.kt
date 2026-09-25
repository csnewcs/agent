package com.agent.health.ui

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.PowerManager
import android.provider.Settings
import android.widget.Toast
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
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
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.BatteryAlert
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Favorite
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.Link
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Schedule
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material3.Divider
import androidx.compose.material3.Icon
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.agent.health.data.HealthConnectManager
import com.agent.health.data.PreferencesManager
import com.agent.health.network.WebhookSender
import com.agent.health.worker.HealthSyncWorker
import kotlinx.coroutines.launch
import java.text.SimpleDateFormat
import java.time.Instant
import java.util.Date
import java.util.Locale

// Samsung OneUI Dark Palette
private val OneUIBackground = Color(0xFF121214)
private val OneUICardBackground = Color(0xFF1E1E24)
private val OneUIInnerCard = Color(0xFF282830)
private val OneUIPrimaryText = Color(0xFFEEEEEE)
private val OneUISecondaryText = Color(0xFF9E9EA8)
private val OneUIAccentBlue = Color(0xFF4C8CFF)
private val OneUIGreen = Color(0xFF34C759)
private val OneUIRed = Color(0xFFFF5252)
private val OneUIDivider = Color(0xFF2E2E36)

@Composable
fun MainScreen(
    onRequestHealthPermissions: () -> Unit
) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val prefs = remember { PreferencesManager(context) }
    val healthManager = remember { HealthConnectManager(context) }
    val webhookSender = remember { WebhookSender() }

    var webhookUrl by remember { mutableStateOf(prefs.webhookUrl) }
    var intervalInput by remember { mutableStateOf(prefs.intervalMinutes.toString()) }
    var isSyncEnabled by remember { mutableStateOf(prefs.isSyncEnabled) }
    var hasPermissions by remember { mutableStateOf(false) }
    var isBatteryIgnoring by remember { mutableStateOf(checkBatteryOptimization(context)) }
    var lastSyncText by remember { mutableStateOf(formatTimestamp(prefs.lastSyncTimestamp)) }
    var isSendingTest by remember { mutableStateOf(false) }
    var validationError by remember { mutableStateOf<String?>(null) }

    val logs = remember { mutableStateListOf<String>() }

    fun addLog(msg: String) {
        val time = SimpleDateFormat("HH:mm:ss", Locale.getDefault()).format(Date())
        logs.add(0, "$time  $msg")
        if (logs.size > 12) logs.removeLast()
    }

    LaunchedEffect(Unit) {
        hasPermissions = healthManager.hasAllPermissions()
        addLog("시스템 준비 완료. 자동 동기화: ${if (isSyncEnabled) "활성화" else "비활성화"}")
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(OneUIBackground)
            .padding(horizontal = 18.dp)
            .verticalScroll(rememberScrollState())
    ) {
        Spacer(modifier = Modifier.height(44.dp))

        // OneUI Header Title
        Column(modifier = Modifier.padding(vertical = 12.dp)) {
            Text(
                text = "Health Sync",
                color = OneUIPrimaryText,
                fontSize = 28.sp,
                fontWeight = FontWeight.Bold
            )
            Text(
                text = "Mi Fitness 백그라운드 웹훅 동기화",
                color = OneUISecondaryText,
                fontSize = 14.sp
            )
        }

        Spacer(modifier = Modifier.height(12.dp))

        // 1. 상태 요약 및 동기화 스위치 카드 (OneUI Card)
        OneUICard {
            Column(modifier = Modifier.padding(18.dp)) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Column(modifier = Modifier.weight(1f)) {
                        Text(
                            text = if (isSyncEnabled) "백그라운드 동기화 켜짐" else "백그라운드 동기화 꺼짐",
                            color = if (isSyncEnabled) OneUIGreen else OneUISecondaryText,
                            fontSize = 17.sp,
                            fontWeight = FontWeight.Bold
                        )
                        Spacer(modifier = Modifier.height(4.dp))
                        Text(
                            text = "Last Sync: $lastSyncText",
                            color = OneUISecondaryText,
                            fontSize = 13.sp
                        )
                    }
                    Switch(
                        checked = isSyncEnabled,
                        onCheckedChange = { enabled ->
                            val interval = intervalInput.toLongOrNull() ?: 15L
                            if (enabled) {
                                if (webhookUrl.isBlank()) {
                                    Toast.makeText(context, "웹훅 URL을 입력해주세요.", Toast.LENGTH_SHORT).show()
                                    return@Switch
                                }
                                if (interval < PreferencesManager.MIN_INTERVAL_MINUTES) {
                                    intervalInput = "15"
                                    prefs.intervalMinutes = 15L
                                }
                                HealthSyncWorker.scheduleWork(context, prefs.intervalMinutes)
                                isSyncEnabled = true
                                addLog("자동 동기화 시작 (${prefs.intervalMinutes}분 주기)")
                            } else {
                                HealthSyncWorker.cancelWork(context)
                                isSyncEnabled = false
                                addLog("자동 동기화 중지됨")
                            }
                        },
                        colors = SwitchDefaults.colors(
                            checkedThumbColor = Color.White,
                            checkedTrackColor = OneUIAccentBlue,
                            uncheckedThumbColor = OneUISecondaryText,
                            uncheckedTrackColor = OneUIInnerCard
                        )
                    )
                }
            }
        }

        Spacer(modifier = Modifier.height(18.dp))

        // 2. 서버 설정 (Server & Interval)
        OneUISectionTitle("서버 및 수집 설정")
        Spacer(modifier = Modifier.height(6.dp))
        OneUICard {
            Column(modifier = Modifier.padding(16.dp)) {
                // Webhook URL 입력
                Text("Server Webhook URL", color = OneUISecondaryText, fontSize = 12.sp, fontWeight = FontWeight.Bold)
                Spacer(modifier = Modifier.height(6.dp))
                OutlinedTextField(
                    value = webhookUrl,
                    onValueChange = {
                        webhookUrl = it
                        prefs.webhookUrl = it
                    },
                    placeholder = { Text("http://10.0.2.2:5678/webhook", color = OneUISecondaryText) },
                    leadingIcon = { Icon(Icons.Default.Link, contentDescription = null, tint = OneUIAccentBlue) },
                    modifier = Modifier.fillMaxWidth(),
                    singleLine = true,
                    colors = OutlinedTextFieldDefaults.colors(
                        focusedContainerColor = OneUIInnerCard,
                        unfocusedContainerColor = OneUIInnerCard,
                        focusedBorderColor = OneUIAccentBlue,
                        unfocusedBorderColor = Color.Transparent,
                        focusedTextColor = OneUIPrimaryText,
                        unfocusedTextColor = OneUIPrimaryText
                    ),
                    shape = RoundedCornerShape(14.dp)
                )

                Spacer(modifier = Modifier.height(16.dp))

                // 수집 주기 입력
                Text("수집 주기 (분 단위, 최소 15분)", color = OneUISecondaryText, fontSize = 12.sp, fontWeight = FontWeight.Bold)
                Spacer(modifier = Modifier.height(6.dp))
                OutlinedTextField(
                    value = intervalInput,
                    onValueChange = { input ->
                        intervalInput = input
                        val num = input.toLongOrNull()
                        if (num != null) {
                            if (num < PreferencesManager.MIN_INTERVAL_MINUTES) {
                                validationError = "최소 수집 주기는 15분입니다."
                                prefs.intervalMinutes = PreferencesManager.MIN_INTERVAL_MINUTES
                            } else {
                                validationError = null
                                prefs.intervalMinutes = num
                                if (isSyncEnabled) {
                                    HealthSyncWorker.scheduleWork(context, num)
                                }
                            }
                        }
                    },
                    leadingIcon = { Icon(Icons.Default.Schedule, contentDescription = null, tint = OneUIAccentBlue) },
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                    modifier = Modifier.fillMaxWidth(),
                    singleLine = true,
                    isError = validationError != null,
                    colors = OutlinedTextFieldDefaults.colors(
                        focusedContainerColor = OneUIInnerCard,
                        unfocusedContainerColor = OneUIInnerCard,
                        focusedBorderColor = OneUIAccentBlue,
                        unfocusedBorderColor = Color.Transparent,
                        focusedTextColor = OneUIPrimaryText,
                        unfocusedTextColor = OneUIPrimaryText
                    ),
                    shape = RoundedCornerShape(14.dp)
                )

                AnimatedVisibility(visible = validationError != null) {
                    Text(
                        text = validationError ?: "",
                        color = OneUIRed,
                        fontSize = 12.sp,
                        modifier = Modifier.padding(start = 4.dp, top = 4.dp)
                    )
                }
            }
        }

        Spacer(modifier = Modifier.height(18.dp))

        // 3. 동기화 실행 & 권한 설정 (Sync & Permissions)
        OneUISectionTitle("동기화 동작 및 권한")
        Spacer(modifier = Modifier.height(6.dp))
        OneUICard {
            Column {
                // Sync Now (즉시 동기화 버튼)
                OneUIRowItem(
                    icon = Icons.Default.PlayArrow,
                    title = if (isSendingTest) "동기화 진행 중..." else "지금 동기화 (Sync Now)",
                    subtitle = "최근 N분간의 데이터를 읽어 즉시 웹훅 전송",
                    iconColor = OneUIAccentBlue,
                    onClick = {
                        if (webhookUrl.isBlank()) {
                            Toast.makeText(context, "웹훅 URL을 입력해주세요.", Toast.LENGTH_SHORT).show()
                            return@OneUIRowItem
                        }
                        scope.launch {
                            isSendingTest = true
                            addLog("즉시 동기화 시작 (지난 24시간 범위)")
                            val now = Instant.now()
                            val start = now.minusSeconds(24 * 3600) // 지난 24시간 전체 조회!
                            val data = healthManager.readHealthData(start, now)
                            addLog("디버그: ${data.debugInfo}")
                            val res = webhookSender.sendHealthData(webhookUrl, data)
                            isSendingTest = false
                            if (res.isSuccess) {
                                prefs.lastSyncTimestamp = now.toEpochMilli()
                                lastSyncText = formatTimestamp(now.toEpochMilli())
                                addLog("성공: 수면 ${data.sleepSessionsCount}개, 심박 ${data.heartRatesCount}개, 산소 ${data.oxygenSaturationsCount}개, 걸음 ${data.stepsCount}개")
                                Toast.makeText(context, "동기화 성공! (수면: ${data.sleepSessionsCount}개, 심박: ${data.heartRatesCount}개, 걸음: ${data.stepsCount}개)", Toast.LENGTH_LONG).show()
                            } else {
                                val err = res.exceptionOrNull()?.message ?: "오류 발생"
                                addLog("동기화 실패: $err")
                                Toast.makeText(context, "실패: $err", Toast.LENGTH_LONG).show()
                            }
                        }
                    }
                )

                // Health Connect 권한 (미완료 시만 노출)
                if (!hasPermissions) {
                    Divider(color = OneUIDivider, thickness = 0.8.dp)
                    OneUIRowItem(
                        icon = Icons.Default.Favorite,
                        title = "Health Connect 권한 설정",
                        subtitle = "권한이 필요합니다 (터치하여 허용)",
                        iconColor = OneUIAccentBlue,
                        onClick = {
                            onRequestHealthPermissions()
                        }
                    )
                }

                // 삼성 절전 기능 예외 (미완료 시만 노출)
                if (!isBatteryIgnoring) {
                    Divider(color = OneUIDivider, thickness = 0.8.dp)
                    OneUIRowItem(
                        icon = Icons.Default.BatteryAlert,
                        title = "삼성 절전 기능 예외 앱 지정",
                        subtitle = "앱 삭제 시에도 백그라운드 유지 설정",
                        iconColor = Color(0xFFFF9800),
                        onClick = {
                            requestIgnoreBatteryOptimization(context)
                            isBatteryIgnoring = checkBatteryOptimization(context)
                            addLog("삼성 배터리 최적화 예외 요청")
                        }
                    )
                }
            }
        }

        Spacer(modifier = Modifier.height(18.dp))

        // 4. 상세 로그 카드 (Detailed Logs)
        OneUISectionTitle("상세 로그 (Detailed Logs)")
        Spacer(modifier = Modifier.height(6.dp))
        OneUICard {
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(16.dp)
            ) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Icon(Icons.Default.Terminal, contentDescription = null, tint = OneUISecondaryText, modifier = Modifier.size(18.dp))
                    Spacer(modifier = Modifier.width(6.dp))
                    Text("실시간 백그라운드 동작 로그", color = OneUISecondaryText, fontSize = 12.sp)
                }
                Spacer(modifier = Modifier.height(10.dp))
                Box(
                    modifier = Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(12.dp))
                        .background(OneUIInnerCard)
                        .padding(12.dp)
                ) {
                    if (logs.isEmpty()) {
                        Text(
                            text = "로그 기록이 없습니다.",
                            color = OneUISecondaryText,
                            fontSize = 12.sp,
                            fontFamily = FontFamily.Monospace
                        )
                    } else {
                        Column {
                            logs.forEach { log ->
                                Text(
                                    text = log,
                                    color = OneUIPrimaryText,
                                    fontSize = 12.sp,
                                    fontFamily = FontFamily.Monospace,
                                    lineHeight = 16.sp,
                                    modifier = Modifier.padding(vertical = 2.dp)
                                )
                            }
                        }
                    }
                }
            }
        }

        Spacer(modifier = Modifier.height(32.dp))
    }
}

@Composable
private fun OneUISectionTitle(title: String) {
    Text(
        text = title,
        color = OneUISecondaryText,
        fontSize = 13.sp,
        fontWeight = FontWeight.Bold,
        modifier = Modifier.padding(start = 6.dp)
    )
}

@Composable
private fun OneUICard(content: @Composable () -> Unit) {
    Box(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(22.dp))
            .background(OneUICardBackground)
    ) {
        content()
    }
}

@Composable
private fun OneUIRowItem(
    icon: ImageVector,
    title: String,
    subtitle: String,
    iconColor: Color,
    onClick: () -> Unit
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable { onClick() }
            .padding(horizontal = 16.dp, vertical = 14.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        Box(
            modifier = Modifier
                .size(38.dp)
                .clip(CircleShape)
                .background(iconColor.copy(alpha = 0.15f)),
            contentAlignment = Alignment.Center
        ) {
            Icon(
                imageVector = icon,
                contentDescription = null,
                tint = iconColor,
                modifier = Modifier.size(20.dp)
            )
        }
        Spacer(modifier = Modifier.width(14.dp))
        Column(modifier = Modifier.weight(1f)) {
            Text(
                text = title,
                color = OneUIPrimaryText,
                fontSize = 15.sp,
                fontWeight = FontWeight.SemiBold
            )
            Text(
                text = subtitle,
                color = OneUISecondaryText,
                fontSize = 12.sp
            )
        }
    }
}

private fun checkBatteryOptimization(context: Context): Boolean {
    val powerManager = context.getSystemService(Context.POWER_SERVICE) as? PowerManager
    return powerManager?.isIgnoringBatteryOptimizations(context.packageName) ?: false
}

private fun requestIgnoreBatteryOptimization(context: Context) {
    try {
        val intent = Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS).apply {
            data = Uri.parse("package:${context.packageName}")
        }
        context.startActivity(intent)
    } catch (e: Exception) {
        val intent = Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS)
        context.startActivity(intent)
    }
}

private fun formatTimestamp(timestamp: Long): String {
    if (timestamp == 0L) return "기록 없음"
    val sdf = SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.getDefault())
    return sdf.format(Date(timestamp))
}
