package com.agent.health.data

import android.content.Context
import android.util.Log
import androidx.health.connect.client.HealthConnectClient
import androidx.health.connect.client.permission.HealthPermission
import androidx.health.connect.client.records.ActiveCaloriesBurnedRecord
import androidx.health.connect.client.records.ExerciseSessionRecord
import androidx.health.connect.client.records.HeartRateRecord
import androidx.health.connect.client.records.HeartRateVariabilityRmssdRecord
import androidx.health.connect.client.records.OxygenSaturationRecord
import androidx.health.connect.client.records.RestingHeartRateRecord
import androidx.health.connect.client.records.SleepSessionRecord
import androidx.health.connect.client.records.StepsRecord
import androidx.health.connect.client.records.Vo2MaxRecord
import androidx.health.connect.client.records.WeightRecord
import androidx.health.connect.client.request.ReadRecordsRequest
import androidx.health.connect.client.time.TimeRangeFilter
import java.time.Duration
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

data class HeartRatePoint(val time: String, val bpm: Long, val type: String = "continuous")
data class OxygenPoint(val time: String, val percentage: Double)
data class ExercisePoint(val title: String, val startTime: String, val endTime: String, val exerciseType: Int)

data class SleepStagePoint(
    val stage: String,
    val stageCode: Int,
    val startTime: String,
    val endTime: String,
    val durationMinutes: Long
)

data class SleepSessionPoint(
    val title: String,
    val startTime: String,
    val endTime: String,
    val totalDurationMinutes: Long,
    val deepSleepMinutes: Long,
    val lightSleepMinutes: Long,
    val remSleepMinutes: Long,
    val awakeMinutes: Long,
    val notes: String,
    val stagesCount: Int,
    val stages: List<SleepStagePoint>
)

data class StepsPoint(val startTime: String, val endTime: String, val count: Long)
data class CaloriesPoint(val startTime: String, val endTime: String, val kcal: Double)
data class WeightPoint(val time: String, val weightKg: Double)
data class Vo2MaxPoint(val time: String, val vo2Max: Double)
data class HrvPoint(val time: String, val hrvRmssd: Double)

data class HealthDataPayload(
    val deviceModel: String = android.os.Build.MODEL,
    val timeZone: String = ZoneId.systemDefault().id,
    val startTime: String,
    val endTime: String,
    val heartRatesCount: Int,
    val oxygenSaturationsCount: Int,
    val exerciseSessionsCount: Int,
    val sleepSessionsCount: Int,
    val stepsCount: Int,
    val activeCaloriesCount: Int,
    val weightRecordsCount: Int,
    val debugInfo: String,
    val heartRates: List<HeartRatePoint>,
    val oxygenSaturations: List<OxygenPoint>,
    val exerciseSessions: List<ExercisePoint>,
    val sleepSessions: List<SleepSessionPoint>,
    val steps: List<StepsPoint>,
    val activeCalories: List<CaloriesPoint>,
    val weightRecords: List<WeightPoint>,
    val vo2MaxRecords: List<Vo2MaxPoint>,
    val hrvRecords: List<HrvPoint>
)

class HealthConnectManager(private val context: Context) {

    private val healthConnectClient by lazy {
        if (HealthConnectClient.getSdkStatus(context) == HealthConnectClient.SDK_AVAILABLE) {
            HealthConnectClient.getOrCreate(context)
        } else {
            null
        }
    }

    val PERMISSIONS = setOf(
        HealthPermission.getReadPermission(HeartRateRecord::class),
        HealthPermission.getReadPermission(RestingHeartRateRecord::class),
        HealthPermission.getReadPermission(OxygenSaturationRecord::class),
        HealthPermission.getReadPermission(ExerciseSessionRecord::class),
        HealthPermission.getReadPermission(SleepSessionRecord::class),
        HealthPermission.getReadPermission(StepsRecord::class),
        HealthPermission.getReadPermission(ActiveCaloriesBurnedRecord::class),
        HealthPermission.getReadPermission(WeightRecord::class),
        HealthPermission.getReadPermission(Vo2MaxRecord::class),
        HealthPermission.getReadPermission(HeartRateVariabilityRmssdRecord::class)
    )

    suspend fun hasAllPermissions(): Boolean {
        val client = healthConnectClient ?: return false
        val granted = client.permissionController.getGrantedPermissions()
        return granted.containsAll(PERMISSIONS)
    }

    suspend fun hasAnyPermission(): Boolean {
        val client = healthConnectClient ?: return false
        val granted = client.permissionController.getGrantedPermissions()
        return granted.any { PERMISSIONS.contains(it) }
    }

    suspend fun readHealthData(startTime: Instant, endTime: Instant): HealthDataPayload {
        val formatter = DateTimeFormatter.ISO_INSTANT
        val debugLogs = mutableListOf<String>()

        val client = healthConnectClient ?: return HealthDataPayload(
            startTime = formatter.format(startTime),
            endTime = formatter.format(endTime),
            heartRatesCount = 0,
            oxygenSaturationsCount = 0,
            exerciseSessionsCount = 0,
            sleepSessionsCount = 0,
            stepsCount = 0,
            activeCaloriesCount = 0,
            weightRecordsCount = 0,
            debugInfo = "HealthConnect SDK Not Available",
            heartRates = emptyList(),
            oxygenSaturations = emptyList(),
            exerciseSessions = emptyList(),
            sleepSessions = emptyList(),
            steps = emptyList(),
            activeCalories = emptyList(),
            weightRecords = emptyList(),
            vo2MaxRecords = emptyList(),
            hrvRecords = emptyList()
        )

        val timeRange = TimeRangeFilter.between(startTime, endTime)

        // 1. 심박수 (HeartRateRecord & RestingHeartRateRecord)
        val heartRateList = mutableListOf<HeartRatePoint>()
        try {
            val hrResponse = client.readRecords(ReadRecordsRequest(HeartRateRecord::class, timeRange))
            debugLogs.add("HR records: ${hrResponse.records.size}")
            for (record in hrResponse.records) {
                for (sample in record.samples) {
                    heartRateList.add(HeartRatePoint(formatter.format(sample.time), sample.beatsPerMinute, "sample"))
                }
            }
        } catch (e: Exception) {
            debugLogs.add("HR err: ${e.message}")
        }

        try {
            val rhrResponse = client.readRecords(ReadRecordsRequest(RestingHeartRateRecord::class, timeRange))
            debugLogs.add("RestingHR: ${rhrResponse.records.size}")
            for (record in rhrResponse.records) {
                heartRateList.add(HeartRatePoint(formatter.format(record.time), record.beatsPerMinute, "resting"))
            }
        } catch (e: Exception) {
            debugLogs.add("RestingHR err: ${e.message}")
        }

        // 2. 산소포화도 (OxygenSaturationRecord)
        val oxygenList = mutableListOf<OxygenPoint>()
        try {
            val oxResponse = client.readRecords(ReadRecordsRequest(OxygenSaturationRecord::class, timeRange))
            debugLogs.add("SpO2: ${oxResponse.records.size}")
            for (record in oxResponse.records) {
                oxygenList.add(OxygenPoint(formatter.format(record.time), record.percentage.value))
            }
        } catch (e: Exception) {
            debugLogs.add("SpO2 err: ${e.message}")
        }

        // 3. 운동 세션 (ExerciseSessionRecord)
        val exerciseList = mutableListOf<ExercisePoint>()
        try {
            val exResponse = client.readRecords(ReadRecordsRequest(ExerciseSessionRecord::class, timeRange))
            debugLogs.add("Exercise: ${exResponse.records.size}")
            for (record in exResponse.records) {
                exerciseList.add(ExercisePoint(record.title ?: "Workout", formatter.format(record.startTime), formatter.format(record.endTime), record.exerciseType))
            }
        } catch (e: Exception) {
            debugLogs.add("Exercise err: ${e.message}")
        }

        // 4. 수면 세션 (SleepSessionRecord + 세부 stages 렘/깊은/얕은/각성 단계 파싱) ⭐
        val rawSleepList = mutableListOf<SleepSessionPoint>()
        try {
            val sleepResponse = client.readRecords(ReadRecordsRequest(SleepSessionRecord::class, timeRange))
            debugLogs.add("SleepRecords: ${sleepResponse.records.size}")
            for (record in sleepResponse.records) {
                val totalMinutes = Duration.between(record.startTime, record.endTime).toMinutes()
                val stageList = mutableListOf<SleepStagePoint>()
                var deepMinutes = 0L
                var lightMinutes = 0L
                var remMinutes = 0L
                var awakeMinutes = 0L

                for (stage in record.stages) {
                    val stageDuration = Duration.between(stage.startTime, stage.endTime).toMinutes()
                    val stageName = when (stage.stage) {
                        SleepSessionRecord.STAGE_TYPE_AWAKE -> {
                            awakeMinutes += stageDuration
                            "AWAKE"
                        }
                        SleepSessionRecord.STAGE_TYPE_LIGHT -> {
                            lightMinutes += stageDuration
                            "LIGHT"
                        }
                        SleepSessionRecord.STAGE_TYPE_DEEP -> {
                            deepMinutes += stageDuration
                            "DEEP"
                        }
                        SleepSessionRecord.STAGE_TYPE_REM -> {
                            remMinutes += stageDuration
                            "REM"
                        }
                        SleepSessionRecord.STAGE_TYPE_SLEEPING -> "SLEEPING"
                        SleepSessionRecord.STAGE_TYPE_OUT_OF_BED -> "OUT_OF_BED"
                        else -> "UNKNOWN"
                    }

                    stageList.add(
                        SleepStagePoint(
                            stage = stageName,
                            stageCode = stage.stage,
                            startTime = formatter.format(stage.startTime),
                            endTime = formatter.format(stage.endTime),
                            durationMinutes = stageDuration
                        )
                    )
                }

                rawSleepList.add(
                    SleepSessionPoint(
                        title = record.title ?: "Sleep",
                        startTime = formatter.format(record.startTime),
                        endTime = formatter.format(record.endTime),
                        totalDurationMinutes = totalMinutes,
                        deepSleepMinutes = deepMinutes,
                        lightSleepMinutes = lightMinutes,
                        remSleepMinutes = remMinutes,
                        awakeMinutes = awakeMinutes,
                        notes = record.notes ?: "",
                        stagesCount = stageList.size,
                        stages = stageList
                    )
                )
            }
        } catch (e: Exception) {
            debugLogs.add("Sleep err: ${e.message}")
        }

        // 중복 세션 정리: 동일 startTime 을 가진 중간 저장본들은 가장 늦은 endTime(완성본) 세션 하나로 스마트하게 통합/필터링
        val sleepList = rawSleepList
            .groupBy { it.startTime }
            .map { (_, sessions) ->
                sessions.maxByOrNull { it.totalDurationMinutes } ?: sessions.first()
            }

        // 5. 걸음 수 (StepsRecord)
        val stepsList = mutableListOf<StepsPoint>()
        try {
            val stepsResponse = client.readRecords(ReadRecordsRequest(StepsRecord::class, timeRange))
            debugLogs.add("Steps: ${stepsResponse.records.size}")
            for (record in stepsResponse.records) {
                stepsList.add(StepsPoint(formatter.format(record.startTime), formatter.format(record.endTime), record.count))
            }
        } catch (e: Exception) {
            debugLogs.add("Steps err: ${e.message}")
        }

        // 6. 활동 칼로리 (ActiveCaloriesBurnedRecord)
        val caloriesList = mutableListOf<CaloriesPoint>()
        try {
            val calResponse = client.readRecords(ReadRecordsRequest(ActiveCaloriesBurnedRecord::class, timeRange))
            debugLogs.add("Calories: ${calResponse.records.size}")
            for (record in calResponse.records) {
                caloriesList.add(CaloriesPoint(formatter.format(record.startTime), formatter.format(record.endTime), record.energy.inKilocalories))
            }
        } catch (e: Exception) {
            debugLogs.add("Calories err: ${e.message}")
        }

        // 7. 체중 (WeightRecord)
        val weightList = mutableListOf<WeightPoint>()
        try {
            val wResponse = client.readRecords(ReadRecordsRequest(WeightRecord::class, timeRange))
            debugLogs.add("Weight: ${wResponse.records.size}")
            for (record in wResponse.records) {
                weightList.add(WeightPoint(formatter.format(record.time), record.weight.inKilograms))
            }
        } catch (e: Exception) {
            debugLogs.add("Weight err: ${e.message}")
        }

        // 8. VO2 Max
        val vo2List = mutableListOf<Vo2MaxPoint>()
        try {
            val vo2Response = client.readRecords(ReadRecordsRequest(Vo2MaxRecord::class, timeRange))
            debugLogs.add("VO2Max: ${vo2Response.records.size}")
            for (record in vo2Response.records) {
                vo2List.add(Vo2MaxPoint(formatter.format(record.time), record.vo2MillilitersPerMinuteKilogram))
            }
        } catch (e: Exception) {
            debugLogs.add("VO2Max err: ${e.message}")
        }

        // 9. HRV (심박변이도)
        val hrvList = mutableListOf<HrvPoint>()
        try {
            val hrvResponse = client.readRecords(ReadRecordsRequest(HeartRateVariabilityRmssdRecord::class, timeRange))
            debugLogs.add("HRV: ${hrvResponse.records.size}")
            for (record in hrvResponse.records) {
                hrvList.add(HrvPoint(formatter.format(record.time), record.heartRateVariabilityMillis))
            }
        } catch (e: Exception) {
            debugLogs.add("HRV err: ${e.message}")
        }

        return HealthDataPayload(
            startTime = formatter.format(startTime),
            endTime = formatter.format(endTime),
            heartRatesCount = heartRateList.size,
            oxygenSaturationsCount = oxygenList.size,
            exerciseSessionsCount = exerciseList.size,
            sleepSessionsCount = sleepList.size,
            stepsCount = stepsList.size,
            activeCaloriesCount = caloriesList.size,
            weightRecordsCount = weightList.size,
            debugInfo = debugLogs.joinToString(" | "),
            heartRates = heartRateList,
            oxygenSaturations = oxygenList,
            exerciseSessions = exerciseList,
            sleepSessions = sleepList,
            steps = stepsList,
            activeCalories = caloriesList,
            weightRecords = weightList,
            vo2MaxRecords = vo2List,
            hrvRecords = hrvList
        )
    }

    fun isHealthConnectAvailable(): Boolean {
        return HealthConnectClient.getSdkStatus(context) == HealthConnectClient.SDK_AVAILABLE
    }
}
