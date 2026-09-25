import tempfile
import queue
import unittest
from pathlib import Path
from unittest.mock import patch

import main as proxy


class FakeApp:
    def __init__(self):
        self.calls = []

    def rpc(self, method, params):
        self.calls.append((method, params))
        if method == "thread/start":
            return {"thread": {"id": "thread-1"}}
        if method == "turn/start":
            return {"turn": {"id": f"turn-{len(self.calls)}"}}
        return {}


class StartTurnPreferencesTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.fake_app = FakeApp()
        self.app_patch = patch.object(proxy, "app", self.fake_app)
        self.workspace_patch = patch.object(proxy, "WORKSPACE_BASE", Path(self.temporary.name))
        self.state_patch = patch.object(proxy, "STATE_FILE", Path(self.temporary.name) / "state.json")
        for active_patch in (self.app_patch, self.workspace_patch, self.state_patch):
            active_patch.start()
            self.addCleanup(active_patch.stop)
        proxy.sessions.clear()
        self.addCleanup(proxy.sessions.clear)

    def test_omitted_settings_keep_selected_values(self):
        proxy.start_turn({
            "sessionId": "codex_proj_work", "projectId": "work", "prompt": "first",
            "model": "gpt-6-sol", "effort": "xhigh",
        })
        session = proxy.start_turn({"sessionId": "codex_proj_work", "prompt": "next"})

        self.assertEqual((session.project_id, session.model, session.effort), ("work", "gpt-6-sol", "xhigh"))
        method, params = self.fake_app.calls[-1]
        self.assertEqual(method, "turn/start")
        self.assertEqual((params["model"], params["effort"]), ("gpt-6-sol", "xhigh"))

    def test_explicit_default_clears_saved_settings(self):
        proxy.start_turn({
            "sessionId": "codex_proj_work", "projectId": "work", "prompt": "first",
            "model": "gpt-6-sol", "effort": "xhigh",
        })
        session = proxy.start_turn({
            "sessionId": "codex_proj_work", "prompt": "reset", "model": "", "effort": "",
        })

        self.assertEqual((session.model, session.effort), ("", ""))
        _, params = self.fake_app.calls[-1]
        self.assertNotIn("model", params)
        self.assertNotIn("effort", params)


class AgentMessagePhaseTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.state_patch = patch.object(proxy, "STATE_FILE", Path(self.temporary.name) / "state.json")
        self.state_patch.start()
        self.addCleanup(self.state_patch.stop)
        proxy.sessions.clear()
        self.addCleanup(proxy.sessions.clear)
        self.app = proxy.CodexAppServer()
        self.session = proxy.Session(session_id="test", project_id="work", thread_id="thread-1")
        self.events = queue.Queue()
        self.session.subscribers.append(self.events)
        proxy.sessions[self.session.session_id] = self.session

    def notify(self, method, **params):
        self.app._handle_notification(method, {"threadId": "thread-1", **params})

    def test_commentary_is_visible_while_running_but_removed_on_completion(self):
        self.notify("item/agentMessage/delta", itemId="progress", delta="진행 중")
        self.notify("item/completed", item={"type": "agentMessage", "id": "progress", "phase": "commentary", "text": "진행 중"})
        self.assertEqual(self.session.final_response, "")
        self.assertEqual(self.events.get_nowait()["output"], "진행 중")

        self.notify("item/agentMessage/delta", itemId="answer", delta="최종 답변")
        self.notify("item/completed", item={"type": "agentMessage", "id": "answer", "phase": "final_answer", "text": "최종 답변"})
        self.notify("turn/completed", turn={"status": "completed"})

        self.assertEqual("".join(self.session.chat), "진행 중최종 답변")
        self.assertEqual(self.session.final_response, "최종 답변")
        self.assertEqual(self.events.get_nowait()["type"], "chat")
        completed = self.events.get_nowait()
        self.assertEqual((completed["type"], completed["output"]), ("completed", "최종 답변"))

    def test_unphased_messages_use_only_the_last_completed_item(self):
        for item_id, message in (("progress", "진행 중"), ("answer", "최종 답변")):
            self.notify("item/agentMessage/delta", itemId=item_id, delta=message)
            self.notify("item/completed", item={"type": "agentMessage", "id": item_id, "text": message})
        self.notify("turn/completed", turn={"status": "completed"})

        self.assertEqual(self.session.final_response, "최종 답변")
        events = list(self.events.queue)
        self.assertEqual(events[-1]["output"], "최종 답변")


if __name__ == "__main__":
    unittest.main()
