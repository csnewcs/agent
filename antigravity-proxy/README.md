# antigravity-proxy

`antigravity-proxy`는 봇(Bot) 프로세스와 Antigravity 엔진 사이에서 입출력을 중계하는 단일 프로세스 프록시 서비스입니다.

## 주요 기능

1. **단일 프로세스 상주**: 지속적으로 켜져 있으면서 `STDIN`을 통해 요청을 수신하고 `STDOUT`으로 스트리밍 응답을 내보냅니다.
2. **세션 자동 발급 및 관리**: `sessionId`가 없는 경우 고유 세션 ID(`ag_sess_...`)를 자동 생성하고 대화 컨텍스트를 유지합니다.
3. **타입 세분화 스트리밍**:
   - `thinking`: 에이전트 추론 과정
   - `chat`: 대화 및 답변 텍스트
   - `tool`: 실시간 도구(Shell, 파일 생성 등) 실행
   - `completed`: 세션 처리 완료
   - `cancelled`: 실행 취소
   - `permission_request`: 고위험 도구 승인 요청
   - `error`: 예외 처리
4. **세션 작업 취소 (`action: "cancel"`)**: 진행 중인 에이전트 프로세스를 즉시 SIGKILL로 중단.
5. **프로젝트 작업 공간 격리 (Workspace Isolation)**: `projectId` 별 독립된 작업 디렉토리(`/tmp/antigravity_workspaces/<projectId>`) 자동 생성 및 샌드박스 실행.
6. **권한 승인 대기 (`permission_request` / `permission_response`)**: 봇 사용자에게 승인 여부를 전송받아 이어서 진행하는 인터랙션 지원.

---

## 입출력 데이터 규격

### 1. 일반 프롬프트 요청 (STDIN)
```json
{
  "sessionId": "",
  "projectId": "hello-c-demo",
  "prompt": "Hello, world!를 출력하는 프로젝트를 C언어로 만들어줘"
}
```

### 2. 세션 취소 요청 (STDIN)
```json
{
  "action": "cancel",
  "sessionId": "ag_sess_396893c401"
}
```

### 3. 사용자 권한 승인 응답 (STDIN)
```json
{
  "action": "permission_response",
  "sessionId": "ag_sess_396893c401",
  "approved": true
}
```

---

## 스트리밍 응답 포맷 (STDOUT)

```json
{"sessionId": "ag_sess_396893c401", "type": "thinking", "tool": "", "output": "빌드 환경 구상 중..."}
{"sessionId": "ag_sess_396893c401", "type": "tool", "tool": "write_to_file", "output": "{\"TargetFile\": \"main.c\"}"}
{"sessionId": "ag_sess_396893c401", "type": "tool", "tool": "shell", "output": "{\"CommandLine\": \"gcc -o hello main.c && ./hello\"}"}
{"sessionId": "ag_sess_396893c401", "type": "chat", "tool": "", "output": "C언어 프로그램 생성을 완료했습니다."}
{"sessionId": "ag_sess_396893c401", "type": "completed", "tool": "", "output": ""}
```

## 실행 방법
```bash
./antigravity-proxy/antigravity-proxy
```
