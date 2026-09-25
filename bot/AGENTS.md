# AI Agent & Discord Bot Development Guidelines

이 문서는 이 레포지토리의 AI Agent 및 Discord Bot 개발 시 지켜야 할 아키텍처, UI/UX 출력 규격, API 연동 표준을 정리한 가이드라인입니다.

---

## 🎨 1. Discord Components V2 출력 UI/UX 표준 규격

Discord 메시지 전송 및 수정 시 구형 `MessageEmbed`를 지양하고, **Discord Components V2** 최신 규격을 준수합니다.

### 🚫 금지 사항
1. **구형 Embed(`MessageEmbed`) 사용 금지**: `Embeds: nil`로 설정하고 Components V2 컴포넌트로만 구성합니다.
2. **`MediaGallery`를 `Container`로 감싸지 말 것**:
   * `Container` 내부에 `MediaGallery`를 배치하면 카드 테두리와 패딩/마진으로 인해 이미지가 강제로 축소됩니다.
   * `MediaGallery`는 반드시 **최상위(Top-Level) 컴포넌트**로 배치하여 디스코드 채팅창 가로폭을 100% 꽉 채우도록 합니다.

### 📐 표준 컴포넌트 계층 구조 (Standard Hierarchy)
```
1. [TextDisplay]   ### 🎵 {메인 제목 / 곡명}
                   **{서브 제목 / 아티스트 / 설명}**

2. [Separator]     ────────────────────────────────────────── (Divider: true, Spacing: Small)

3. [MediaGallery]  ┌────────────────────────────────────────┐
   (Top-Level)     │                                        │
                   │        가사/결과 이미지 (가로폭 100%)       │
                   │                                        │
                   └────────────────────────────────────────┘
   또는 [TextDisplay] (텍스트 모드일 때 본문)

4. [Separator]     ────────────────────────────────────────── (Divider: true, Spacing: Small)

5. [TextDisplay]   -# {소스/상태} • {모델명} | {진행도 / 시간 정보}

6. [ActionsRow]    [ ⏪ 이전 ] [ 🔄 새로고침 ] [ ⏩ 다음 ] [ ⏹️ 중지 ]
```

---

## ⚠️ 2. Discord REST API 첨부파일 수정 (PATCH) 시 필수 규칙

### 🛑 `HTTP 400 Code 30015` 방지 (첨부파일 누적 문제)
* 디스코드 API는 메시지를 수정(PATCH)할 때 `attachments` 배열을 명시하지 않고 새 파일(`files`)만 전송하면 **기존 첨부파일을 삭제하지 않고 계속 누적**합니다.
* 10회 수정(10초)이 지나면 최대 첨부파일 한도(10개)에 걸려 `Maximum number of allowed attachments in a message reached (10)` 에러가 발생하며 모든 업데이트가 멈춥니다.
* **해결 및 필수 구현**: 텍스트 모드로 바꿀 때는 빈 `attachments`를 보내 기존 이미지를 제거합니다. 새 이미지를 올릴 때는 이전 첨부파일을 제외하고 **새 파일의 `id`(파일 인덱스)와 `filename`을** `attachments`에 넣어 1장만 유지합니다. 파일이 있어도 빈 배열을 보내면 새 이미지까지 제거될 수 있습니다.
  ```go
  replacementAttachments := []*discordgo.MessageAttachment{
      {ID: "0", Filename: "lyrics.jpg"},
  }

  // 1. Webhook Edit 시
  editV2 := WebhookEditLyricsV2{
      Components:  &allComponents,
      Flags:       discordgo.MessageFlagsIsComponentsV2,
      Attachments: &replacementAttachments,
  }

  // 2. Channel Message Edit 시
  edit := &discordgo.MessageEdit{
      Channel:     channelID,
      ID:          messageID,
      Components:  &allComponents,
      Flags:       discordgo.MessageFlagsIsComponentsV2,
      Files:       files,
      Attachments: &replacementAttachments,
  }
  ```
* 웹훅 업로드가 실패하여 봇 토큰 편집으로 폴백할 때는 파일 `Reader`를 처음으로 되감거나 파일을 다시 열어야 합니다. 이미 소비된 Reader를 재사용하면 0바이트 파일이 올라갑니다.

---

## 🔑 3. 인터랙션 웹훅 우선순위 (403 Missing Access 방지)

* 인터랙션(`/` 슬래시 커맨드 또는 버튼 클릭)으로 생성된 메시지는 채널 레벨의 봇 메시지 권한(`Missing Access`)에 구애받지 않고 **고유 Interaction Token(`@original`)** 으로 수정할 수 있습니다.
* 메시지 수정 시 **Interaction Webhook(`PATCH .../@original`)을 1순위로 시도**하고, 15분 후 토큰 만료 시에만 봇 토큰 기반 `ChannelMessageEditComplex`로 폴백합니다.
* 수정 실패 시 `FollowupMessageCreate` 등으로 새 메시지를 반복 생성하는 Fallback을 두지 않습니다 (중복 메시지 폭주 방지).

---

## 🖼️ 4. 가사 카드 이미지 렌더러 (Go Native) 규격

* **해상도**: `1600 × 640 px`
* **폰트 체인 (Rune 단위 Fallback)**:
  1. `Paperlogy-8ExtraBold.ttf` (메인 국문/영문/숫자)
  2. `NotoSansKR-Bold.ttf` (누락 한글 음절)
  3. `NotoSansJP-Bold.ttf` (일본어 한자/히라가나/가타카나)
* **타이포그래피 크기 및 색상 팔레트**:
  * **현재 활성 가사 (Main)**: `60pt` (ExtraBold, `#FFFFFF`)
  * **한국어 번역 (Translation)**: `36pt` (Bold, `#CCD0DC`)
  * **발음 표기 (Phonetic)**: `32pt` (Bold, `#CDAAFF` 라일락/보라 강조)
  * **주변 가사 (Context -2, -1, +1, +2)**: `30pt` ~ `36pt` (Dimmed)
  * **상단 프로그레스 바**: `9px` 높이, `#AF87F5` (라벤더/보라)
* **배경 블러 및 명도**: `gift.GaussianBlur(38)` + `gift.Brightness(-42)` + `gift.Contrast(-10)` 적용으로 어떤 앨범 커버에서도 완벽한 텍스트 가독성 확보
* **포맷 및 MIME**: Go 표준 `image/jpeg` (Quality: 86~88) 인코딩, `lyrics.jpg` (`image/jpeg`)로 전송하여 디스코드 클라이언트와의 100% 호환성 유지.

---

## 🚀 5. 봇 배포 요청의 완료 기준

* 사용자가 수정 사항의 **배포까지 요청**했다면 코드 수정이나 테스트 통과만으로 완료를 선언하지 않습니다. 변경 범위와 배포 대상을 확인한 뒤, 필요한 서비스만 빌드하고 배포합니다.
* 배포 명령이 끝날 때까지 현재 작업에서 기다리고, 컨테이너의 새 이미지 적용 여부·실행 상태·시작 로그 및 관련 기능 테스트를 확인한 후 최종 결과를 보고합니다. 확인 중에는 적절히 진행 상황을 알립니다.
* 시간이 걸리는 배포를 지켜본다는 뜻의 "타이머"는 별도 예약 알림을 만들라는 뜻이 아닙니다. 배포가 성공했는지 확인하기 전에 작업을 끝내거나 성공했다고 표현하지 않습니다.
* 실제 Discord 상호작용을 재현하지 못했다면 확인한 범위를 분명히 구분해서 보고합니다.
* 재시작 복구 기능을 배포할 때 현재 실행 중인 **구버전 Codex 세션**에 저장된 Discord 메시지 바인딩이 없다면 컨테이너 재시작으로 그 응답이 끊길 수 있습니다. 해당 세션이 끝났는지 확인하고 배포하며, 활성 상태라면 빌드 완료와 실제 배포를 구분해 보고합니다.
