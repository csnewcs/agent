# AI Agent Integration Hub (Discord Bot & KEPCO API)

이 프로젝트는 개인 사용자의 AI Agent 진입점인 **Discord Go Bot**과 한국전력 실시간 전력 사용량 조회를 위한 **NestJS KEPCO API Server**가 통합된 시스템입니다. n8n 워크플로우와 연동하여 동작하며, 전체 인프라는 Docker Compose를 통해 편리하게 구동할 수 있습니다.

---

## 📂 프로젝트 구조

* **`bot/`**: Go 언어로 구현된 Discord 봇입니다. 
  * 사용자 멘션 또는 슬래시 커맨드를 통해 입력된 질문을 n8n Webhook으로 중계합니다.
  * 대화 내역은 PostgreSQL DB(`agent` 데이터베이스)에 영구 저장하며 최신 30개의 히스토리만 유지합니다.
* **`kepco/`** *(Git Submodule - Upstream: `noeulnight/kepco`)*: NestJS 기반 한국전력 실시간 요금 및 사용량 조회 API 서버입니다. (Docker 환경에서는 GHCR 게시 이미지 `ghcr.io/noeulnight/kepco:latest`를 직접 불러와 사용합니다.)
* **`docker-compose.yml`**: 로컬 DB 및 외부 API와 충돌 없이 통신하도록 `network_mode: host` 기반으로 설계된 멀티 컨테이너 오케스트레이션 정의 파일입니다.

---

## 🛠️ 환경 설정 (.env)

컨테이너 구동 전 각 폴더의 `.env` 설정 파일 확인이 필요합니다. (깃허브 커밋 대상에서 보안상 제외되어 있습니다.)

### 1. `bot/.env`
```env
TOKEN="YOUR_DISCORD_BOT_TOKEN"
MODE="production"
DEFAULT_SERVER_ID="YOUR_GUILD_ID"
DEFAULT_CHANNEL_ID="YOUR_CHANNEL_ID"
N8N_WEBHOOK_URL="http://localhost:5678/webhook/discord"
N8N_TEST_WEBHOOK_URL="http://localhost:5678/webhook-test/discord"
OPENAI_API_KEY="YOUR_OPENAI_API_KEY"
KEPCO_ID="YOUR_KEPCO_ID"
KEPCO_PW="YOUR_KEPCO_PASSWORD"
DATABASE_URL="postgres://agent@localhost:5432/agent?sslmode=disable"
```

### 2. `kepco/.env`
```env
KEPCO_ID="YOUR_KEPCO_ID"
KEPCO_PW="YOUR_KEPCO_PASSWORD"
PORT=4000
CORS_ORIGIN=*
```

---

## 🚀 실행 및 빌드 방법 (Docker Compose)

프로젝트 루트 디렉토리에서 아래 명령어를 실행하여 컨테이너 환경으로 통합 빌드 및 실행을 진행합니다.

```bash
# 컨테이너 통합 빌드 및 백그라운드 실행
docker compose up --build -d

# 서비스 실시간 로그 확인
docker compose logs -f

# 서비스 정지 및 컨테이너 삭제
docker compose down
```

---

## 🤖 Discord Slash Commands

봇 구동 시 디스코드 서버 내에 아래 명령어들이 자동 등록됩니다.

* **`/kepco`**: 한국전력의 실시간 전력 사용량 및 요금 정보를 모노스페이스 블록 형태로 깔끔하게 정렬하여 출력합니다.
* **`/stats`**: 서버 시스템 상태, 1.1.1.1 핑 속도, 그리고 오늘 하루 OpenAI 토큰 사용량을 표시합니다.
  * 토큰 사용량은 일일 한도 **2.5M 토큰을 100% 기준**으로 백분율(예: `(0.08%)`)을 계산하여 표시합니다.
* **`/delete_session`**: 다중 세션을 일괄 삭제할 수 있는 기능입니다.
  * 명령어 실행자 본인에게만 메시지가 보이는 **비공개(Ephemeral) 응답** 형태로 결과가 통보됩니다.
  * 현재 활성화된 세션이 삭제 대상에 들어갈 경우 안전하게 백업 세션으로 변경을 보장합니다.
* **`/ask`**: n8n AI Agent 워크플로우에 질문을 보내 상호작용합니다.
  * `query`: 질문 내용 (필수)
  * `session`: 세션 선택 (선택)
  * `ephemeral`: 응답 비공개 여부 (미선택 시 답변 출력 길이 100자 이하 전체 공개, 100자 초과 비공개)
* **`/gohome`**: 오늘 퇴근까지 남은 시간을 알려줍니다.
  * `person`: 인원 선택 (드롭다운: `배재현`(기본값 - 주중 18:00), `임태현`(주중 17:00), `박민혁`(주중 22:00 / 토·공휴일 18:00))
* **`/c`**: 호스트(도커 밖 리눅스 OS) 환경에서 셸 커맨드를 직접 실행하고 결과를 반환합니다.
  * `command`: 실행할 커맨드 (필수)
* **`/weather`**: 웹훅 서비스로부터 실시간 기상 정보(날씨, 기온, 체감온도, 습도, 강수량, 풍속/풍향)를 조회하여 임베드 형태로 보여줍니다.
  * `location`: 날씨를 조회할 위치/지역명 (선택, 미입력 시 기본값: `성남시 수정구 태평1동`)
* **`/forecast`**: 웹훅 서비스로부터 실시간 단기예보 정보(시간별 예보, 날씨, 기온, 체감온도, 습도, 강수확률, 강수량)를 조회하여 임베드 형태로 보여줍니다.
  * `location`: 단기예보를 조회할 위치/지역명 (선택, 미입력 시 기본값: `성남시 수정구 태평1동`)
* **`/tj`**: TJ 노래방 트래킹 대상(아티스트 및 곡 제목)을 추가, 삭제, 조회합니다.
  * `/tj add category:<artist|song> name:"제목/아티스트명"`: 트래킹 대상 추가
  * `/tj delete category:<artist|song> name:"제목/아티스트명"`: 트래킹 대상 삭제
  * `/tj list [category:<all|artist|song>]`: 등록된 트래킹 목록 조회
