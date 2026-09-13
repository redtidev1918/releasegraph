from __future__ import annotations

import argparse
import json
from pathlib import Path

from . import notes
from .assets import AssetError, collect_assets, write_checksums
from .inventory import scan, write_outputs
from .policy import desired_version, load_policy
from .release import assert_no_cleanup_tag, audit, plan, publish, stage


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="releasectl")
    sub = parser.add_subparsers(dest="command", required=True)
    policy_parser = sub.add_parser("policy")
    policy_parser.add_argument("--path", default=".release-policy.yml")
    desired_parser = sub.add_parser("desired-version")
    desired_parser.add_argument("--path", default=".release-policy.yml")
    desired_parser.add_argument("--version")
    desired_parser.add_argument("--root", default=".")
    assets_parser = sub.add_parser("asset-gate")
    assets_parser.add_argument("--path", default=".release-policy.yml")
    assets_parser.add_argument("--root", default="dist/release")
    fleet_parser = sub.add_parser("fleet-audit")
    fleet_parser.add_argument("--owner", required=True)
    fleet_parser.add_argument("--output", default=".")
    fleet_parser.add_argument("--public-only", action="store_true")
    for name in ("stage", "publish", "audit"):
        command_parser = sub.add_parser(name)
        command_parser.add_argument("--path", default=".release-policy.yml")
        command_parser.add_argument("--version")
        if name != "audit":
            command_parser.add_argument("--dry-run", action="store_true")
    static_parser = sub.add_parser("static-check")
    static_parser.add_argument("--root", default=".")
    notes_parser = sub.add_parser("notes")
    notes_parser.add_argument("--path", default=".release-policy.yml")
    notes_parser.add_argument("--version")
    notes_parser.add_argument("--tag")
    notes_parser.add_argument("--root", default=".")
    notes_parser.add_argument("--output")
    plan_parser = sub.add_parser("workflow-plan")
    plan_parser.add_argument("--path", default=".release-policy.yml")
    plan_parser.add_argument("--version")
    plan_parser.add_argument("--force", action="store_true")
    plan_parser.add_argument("--repair", action="store_true")
    plan_parser.add_argument("--github-output")
    args = parser.parse_args(argv)
    if args.command == "policy":
        print(json.dumps(load_policy(args.path), indent=2))
    elif args.command == "desired-version":
        print(desired_version(load_policy(args.path), args.version, args.root))
    elif args.command == "asset-gate":
        policy = load_policy(args.path)
        assets = collect_assets(policy.get("assets", {}).get("required", []), policy.get("assets", {}).get("optional", []), args.root)
        if assets and policy.get("checksums", True):
            write_checksums(assets, Path(args.root) / "SHA256SUMS")
        print("\n".join(str(path) for path in assets))
    elif args.command == "fleet-audit":
        inventory = scan(args.owner, include_private=not args.public_only)
        write_outputs(inventory, args.output)
        print(f"audited {len(inventory)} repositories")
    elif args.command == "stage":
        print(stage(args.path, args.version, dry_run=args.dry_run))
    elif args.command == "publish":
        print(publish(args.path, args.version, dry_run=args.dry_run))
    elif args.command == "audit":
        audit(args.path, args.version)
    elif args.command == "workflow-plan":
        result = plan(args.path, args.version, force=args.force, repair=args.repair)
        if args.github_output:
            with Path(args.github_output).open("a") as output:
                for key, value in result.items():
                    output.write(f"{key}={value}\n")
        else:
            print(json.dumps(result, indent=2))
    elif args.command == "static-check":
        assert_no_cleanup_tag(args.root)
    elif args.command == "notes":
        policy = load_policy(args.path)
        version = desired_version(policy, args.version, args.root)
        tag = args.tag or policy.get("tag", {}).get("template", "v{version}").format(version=version)
        try:
            built = collect_assets(policy.get("assets", {}).get("required", []),
                                   policy.get("assets", {}).get("optional", []))
        except AssetError:
            # A preview before the build produced anything is still useful: the
            # body then explains where the repository actually delivers from.
            built = []
        body = notes.build_for_release(policy, version, tag, root=args.root,
                                       asset_names=[path.name for path in built],
                                       asset_sizes={path.name: path.stat().st_size for path in built})
        if args.output:
            Path(args.output).write_text(body, encoding="utf-8")
        else:
            print(body, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
