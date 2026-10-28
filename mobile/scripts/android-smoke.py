#!/usr/bin/env python3
"""Exercise the installed Android UI without WebView debugging or model keys.

Usage: android-smoke.py --adb /path/to/adb --serial emulator-5554 --apk app.apk
Creates a uniquely named manual test novel. Never clears existing application
data. Screenshots, UI trees, a result JSON, and app logs go to --output.
"""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
import time
import xml.etree.ElementTree as ET


PACKAGE = "org.ainovel.mobile"
COMPONENT = PACKAGE + "/.MainActivity"
# Android WebView accessibility commonly omits HTML element IDs. These are
# labels/classes observed in its accessibility tree, not screen coordinates.
WEB_SELECTORS = {
    "create-project": {"text": "＋ 新建创作项目", "clickable": "true"},
    "new-project-mode": {"text": "创作方式", "clickable": "true"},
    "new-project-name": {"class": "android.widget.EditText"},
    "confirm-create-project": {"text": "创建项目", "clickable": "true"},
    "manual-add-chapter": {"text": "＋ 新建章节", "clickable": "true"},
    "manual-chapter-title": {"class": "android.widget.EditText"},
    "confirm-manual-chapter": {"text": "创建并编辑", "clickable": "true"},
    "edit-chapter-content": {"class": "android.widget.EditText", "min_height": 120},
    "save-chapter-content": {"text": "保存正文为新版本并采用", "clickable": "true"},
}


class Device:
    def __init__(self, args):
        self.args = args
        self.output = Path(args.output).resolve()
        self.output.mkdir(parents=True, exist_ok=True)
        self.stage = "initial"
        self.steps = []

    def adb(self, *arguments, binary=False, timeout=35):
        result = subprocess.run([self.args.adb, "-s", self.args.serial, *arguments],
                                capture_output=True, timeout=timeout, check=True)
        return result.stdout if binary else result.stdout.decode("utf-8", errors="replace").strip()

    def shell(self, *arguments, **kwargs):
        return self.adb("shell", *arguments, **kwargs)

    def tree(self):
        self.shell("uiautomator", "dump", "/sdcard/ainovel-smoke.xml", timeout=20)
        data = self.adb("exec-out", "cat", "/sdcard/ainovel-smoke.xml")
        root = ET.fromstring(data[data.index("<?xml"):])
        return root, data

    def capture(self, name):
        self.stage = name
        (self.output / (name + ".png")).write_bytes(self.adb("exec-out", "screencap", "-p", binary=True))
        try:
            _, data = self.tree()
            (self.output / (name + ".xml")).write_text(data, encoding="utf-8")
        except Exception as error:
            (self.output / (name + ".error.txt")).write_text(str(error), encoding="utf-8")

    @staticmethod
    def bounds(node):
        numbers = list(map(int, re.findall(r"\d+", node.attrib.get("bounds", ""))))
        return numbers if len(numbers) == 4 else [0, 0, 0, 0]

    def find(self, *, text=None, resource=None, contains=None, timeout=20, scroll=False):
        deadline = time.monotonic() + timeout
        last = []
        scrolls = 0
        while time.monotonic() < deadline:
            root, _ = self.tree()
            last = []
            for node in root.iter("node"):
                attrs = node.attrib
                label = attrs.get("text", "") or attrs.get("content-desc", "")
                if label:
                    last.append(label)
                x1, y1, x2, y2 = self.bounds(node)
                if x2 <= x1 or y2 <= y1 or attrs.get("enabled") == "false":
                    continue
                if resource and (attrs.get("resource-id", "") == resource or attrs.get("resource-id", "").endswith("/" + resource)):
                    return node
                alias = WEB_SELECTORS.get(resource, {})
                if alias and all((y2-y1 >= value if attr == "min_height" else attrs.get(attr) == value) for attr, value in alias.items()):
                    return node
                if text is not None and label == text:
                    return node
                if contains and contains in label:
                    return node
            if scroll and scrolls < 4:
                self.scroll(root)
                scrolls += 1
            time.sleep(0.3)
        raise AssertionError(f"UI element missing: text={text!r} resource={resource!r} contains={contains!r}; visible={last}")

    def tap(self, **selector):
        node = self.find(**selector)
        x1, y1, x2, y2 = self.bounds(node)
        self.shell("input", "tap", str((x1+x2)//2), str((y1+y2)//2))
        time.sleep(0.35)
        return node

    def type(self, resource, value):
        if not re.fullmatch(r"[A-Za-z0-9 .,_-]+", value):
            raise ValueError("ADB test input intentionally permits only safe ASCII text")
        if resource == "edit-chapter-content":
            # Let the application reveal and focus the editor. Its large
            # accessibility bounds do not describe the unobscured tap region.
            self.tap(text="编辑")
        node = self.find(resource=resource, scroll=resource == "edit-chapter-content")
        # The manuscript field is focused by the Editor tab. Its accessibility
        # bounds can overlap the sticky toolbar after the keyboard resizes the
        # viewport, so a second center tap may hit a different tab.
        if node.attrib.get("focused") != "true":
            if resource == "edit-chapter-content":
                raise AssertionError("Editor tab did not focus the manuscript field")
            x1, y1, x2, y2 = self.bounds(node)
            self.shell("input", "tap", str((x1+x2)//2), str((y1+y2)//2))
        self.shell("input", "text", value.replace(" ", "%s"))

    def scroll(self, root):
        _, _, width, height = self.bounds(next(root.iter("node")))
        bottom = height
        for node in root.iter("node"):
            if node.attrib.get("text") == "手机快捷导航":
                bottom = min(bottom, self.bounds(node)[1])
        regions = []
        for node in root.iter("node"):
            x1, y1, x2, y2 = self.bounds(node)
            y2 = min(y2, bottom)
            if node.attrib.get("scrollable") == "true" and node.attrib.get("class") != "android.widget.EditText" and x2-x1 > 100 and y2-y1 > 100:
                regions.append((x1, y1, x2, y2))
        if regions:
            x1, y1, x2, y2 = min(regions, key=lambda r: (r[2]-r[0])*(r[3]-r[1]))
            # Swipe the region's side gutter so an inner textarea cannot trap it.
            self.shell("input", "swipe", str(x2-8), str(y2-24), str(x2-8), str(y1+24), "400")
        else:
            self.shell("input", "swipe", str(width*3//4), str(bottom-50), str(width*3//4), str(height//3), "400")

    def project_action(self, project, action):
        """Choose a control in this test's card, preserving existing projects."""
        for attempt in range(5):
            root, _ = self.tree()
            parents = {child: parent for parent in root.iter() for child in parent}
            titles = [node for node in root.iter("node") if node.attrib.get("text") == project and node.attrib.get("class") != "android.widget.EditText"]
            for node in titles:
                while node in parents:
                    node = parents[node]
                    controls = [child for child in node.iter("node") if child.attrib.get("text") == action]
                    for child in controls:
                        if child.attrib.get("enabled") == "false": continue
                        x1, y1, x2, y2 = self.bounds(child)
                        if x2 > x1 and y2 > y1:
                            self.shell("input", "tap", str((x1+x2)//2), str((y1+y2)//2))
                            return
                    if controls:
                        # Stop at this card even when it needs scrolling; a
                        # broader ancestor also contains other projects.
                        break
            self.scroll(root)
        raise AssertionError(f"Project action missing: {project} / {action}")

    def wait_saved(self, content, timeout=25):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            root, _ = self.tree()
            nodes = [node.attrib for node in root.iter("node")]
            editable = any(a.get("class") == "android.widget.EditText" and
                           a.get("text") == content and a.get("enabled") == "true" for a in nodes)
            clean = any(a.get("text") == "保存正文为新版本并采用" and
                        a.get("enabled") == "false" for a in nodes)
            if editable and clean:
                return
            time.sleep(0.3)
        raise AssertionError("Manuscript did not reach the saved, editable state")

    def record(self, name):
        self.steps.append(name)
        print(name, flush=True)

    def logs(self):
        try:
            pid = self.shell("pidof", PACKAGE).split()[0]
            logs = self.adb("logcat", "-d", "--pid="+pid, "-v", "threadtime")
        except Exception:
            logs = self.adb("logcat", "-d", "-s", "AndroidRuntime:E", "Go:E", "chromium:E")
        logs = re.sub(r"(__mobile/session\?token=)[0-9a-f]+", r"\1[REDACTED]", logs)
        (self.output / "app-logcat.txt").write_text(logs, encoding="utf-8")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--adb", default="adb")
    parser.add_argument("--serial", required=True)
    parser.add_argument("--apk", type=Path, required=True)
    parser.add_argument("--output", default="output/android-smoke/" + time.strftime("%Y%m%d-%H%M%S"))
    parser.add_argument("--allow-physical", action="store_true", help="Explicitly allow installing on the named physical device")
    args = parser.parse_args()
    if not args.serial.startswith("emulator-") and not args.allow_physical:
        parser.error("Physical devices require explicit --allow-physical selection")
    device = Device(args)
    project = "AndroidSmoke_" + time.strftime("%m%d_%H%M%S")
    chapter = "Device Chapter"
    content = "This manuscript was written and saved on Android. The story survives an application restart."
    result = {"device": args.serial, "apk": str(args.apk.resolve()),
              "apkSha256": hashlib.sha256(args.apk.read_bytes()).hexdigest(),
              "method": "automated Android UI smoke",
              "project": project, "steps": device.steps, "passed": False}
    try:
        device.adb("install", "-r", str(args.apk.resolve()), timeout=120)
        device.shell("am", "force-stop", PACKAGE)
        device.shell("am", "start", "-n", COMPONENT)
        try:
            device.tap(resource="com.android.permissioncontroller:id/permission_allow_button", timeout=8)
        except AssertionError:
            pass
        device.find(resource="create-project", timeout=50)
        device.capture("01-startup")
        device.record("Signed APK installed; embedded workbench opened")

        device.tap(resource="create-project")
        device.tap(resource="new-project-mode")
        device.tap(contains="纯手动创作")
        device.type("new-project-name", project)
        device.shell("input", "keyevent", "4")
        device.tap(resource="confirm-create-project")
        device.find(resource="manual-add-chapter")
        device.record("Created a manual novel without model credentials")

        device.tap(resource="manual-add-chapter")
        device.type("manual-chapter-title", chapter)
        device.shell("input", "keyevent", "4")
        device.tap(resource="confirm-manual-chapter")
        # The editor has its own scroll region; this toolbar control reveals
        # and focuses the full manuscript field on a narrow phone display.
        device.type("edit-chapter-content", content)
        device.shell("input", "keyevent", "4")
        device.tap(resource="save-chapter-content", scroll=True)
        device.wait_saved(content)
        device.capture("02-saved-chapter")
        device.record("Created a chapter and saved its text through the phone UI")

        device.shell("input", "keyevent", "4")
        device.find(contains=project)
        for format_ in ("TXT", "EPUB"):
            device.project_action(project, format_)
            # Android's system picker owns destination selection and storage
            # permission; the app never needs broad filesystem access.
            try:
                device.tap(text="SAVE", timeout=25)
            except AssertionError:
                device.tap(text="Save", timeout=5)
            device.find(contains=project, timeout=25)
            device.capture("03-export-" + format_.lower())
            device.record("Saved " + format_ + " through Android's document picker")

        device.shell("am", "force-stop", PACKAGE)
        device.shell("am", "start", "-n", COMPONENT)
        device.find(contains=project, timeout=50)
        device.project_action(project, "打开项目")
        # The rendered reader may omit text descendants from accessibility.
        # The editor exposes its complete saved value without changing it.
        device.tap(text="编辑")
        device.find(contains=content, timeout=20)
        device.capture("04-restart-restored")
        device.record("Saved project and chapter survived force-stop and restart")
        result["passed"] = True
    except Exception as error:
        result["error"] = str(error)
        try:
            device.capture("failure")
        except Exception:
            pass
        raise
    finally:
        result["stage"] = device.stage
        try:
            device.logs()
        except Exception as error:
            result["logError"] = str(error)
        (device.output / "result.json").write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")


if __name__ == "__main__":
    main()
