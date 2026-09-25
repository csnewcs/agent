# Agent Health Sync (Android App for Mi Fitness)

Mi Fitness 앱에서 수집한 헬스 데이터(심박수, 산소포화도, 스트레스, 운동 기록)를 Android **Health Connect API**를 통해 백그라운드에서 읽어와 지정된 **웹훅(Webhook) URL**로 자동 전송하는 삼성 갤럭시(Android 16 호환) 안드로이드 애플리케이션입니다.

---

## 📱 주요 기능 및 특징

1. **Mi Fitness 헬스 데이터 연동 (Health Connect)**
   - Android 표준 `Health Connect SDK`를 사용하여 Mi Fitness 앱에서 동기화된 건강 데이터를 읽어옵니다.
   - 수집 항목: 심박수(BPM), 산소포화도(SpO2), 스트레스, 운동 세션 기록.

2. **백그라운드 지속 동기화 (WorkManager + 삼성 절전 예외)**
   - 앱이 최근 앱 목록에서 종료(Kill)되어도 설정된 N분 주기로 백그라운드에서 자동 수집 및 웹훅 전송.
   - 삼성 갤럭시 백그라운드 절전 기능 예외(`REQUEST_IGNORE_BATTERY_OPTIMIZATIONS`) 원클릭 지원.
   - 단말 재부팅 시에도 자동으로 백그라운드 동기화 서비스 재등록(`BOOT_COMPLETED`).

3. **사용자 UI 및 설정 (Jetpack Compose)**
   - **웹훅 URL 설정**: 전송받을 서버의 Webhook URL 지정 및 테스트 전송 기능.
   - **수집 주기 설정**: 분 단위 지정 가능 (**Android OS 정책에 따라 최소 15분으로 강제 제한**).
   - **Health Connect 권한 관리**: 원클릭 권한 동의 및 상태 표시.

---

## 🛠️ 사전 준비 및 설정 가이드

### 1. Mi Fitness 앱 설정
1. Mi Fitness 앱 실행 -> **프로필 / 설정** 진입.
2. **Health Connect** (또는 헬스 커넥트 연동) 메뉴 선택.
3. 데이터 동기화 항목(심박수, 산소포화도, 운동 등)을 **활성화**합니다.

### 2. Agent Health App 설정
1. 앱 설치 및 실행 후 **"권한 요청"** 버튼을 눌러 Health Connect 읽기 권한을 허용합니다.
2. **"삼성 절전 기능 예외 설정"** 버튼을 눌러 배터리 최적화 예외 대상 앱으로 등록합니다.
3. 전송받을 **Webhook URL**을 입력합니다.
4. **수집 주기**를 입력합니다 (최소 15분 이상).
5. **백그라운드 동기화 스위치**를 **켜짐(ON)** 상태로 설정합니다.

---

## 📦 전송되는 JSON 페이로드 구조 예시

`POST` 요청으로 전달되는 JSON Body 구조:

```json
{
  "deviceModel": "SM-S928N",
  "timeZone": "Asia/Seoul",
  "startTime": "2026-08-04T00:00:00Z",
  "endTime": "2026-08-04T00:15:00Z",
  "heartRates": [
    {
      "time": "2026-08-04T00:05:12Z",
      "bpm": 72
    },
    {
      "time": "2026-08-04T00:10:45Z",
      "bpm": 75
    }
  ],
  "oxygenSaturations": [
    {
      "time": "2026-08-04T00:08:00Z",
      "percentage": 98.5
    }
  ],
  "exerciseSessions": [
    {
      "title": "Outdoor Running",
      "startTime": "2026-08-04T00:00:00Z",
      "endTime": "2026-08-04T00:14:30Z",
      "exerciseType": 56
    }
  ]
}
```

---

## 🔨 APK 빌드 방법

`health/` 디렉토리로 이동하여 Gradle 래퍼 또는 로컬 Gradle로 빌드를 실행합니다:

```bash
# Debug APK 빌드
./gradlew assembleDebug

# 빌드 결과물 위치
# app/build/outputs/apk/debug/app-debug.apk
```
