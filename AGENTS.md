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

## 💬 5. 사용자 피드백 및 중간 확인 원칙 (Interactive Check-in)

* **단독 임의 진행 금지**: 새로운 기능 추가, 모델/외부 API 선정, 아키텍처 및 주요 라이브러리 결정 등 설계 및 구현 방향에 선택지가 있거나 중요한 결정이 필요할 때는 AI가 임의로 판단하여 전체 구현까지 일방적으로 진행하지 않습니다.
* **아이디어 제시 후 대기**: 최적의 대안 및 접근 방식을 간결히 정리하여 사용자에게 제시한 후, 거기서 작업을 멈추고 **"이제 어떻게 진행할까요?"** 와 같이 사용자에게 선택 및 진행 방향을 질문합니다.
* **사용자 컨펌 후 구현**: 사용자가 원하는 방향과 모델/도구를 확인해 주면, 그에 맞춰 단계적으로 구현을 시작합니다.
