### assets
```
{assets:[{id:int, name:str, uri:str, group:str, extra:{...}, created_at:str, updated_at:str, scheduled_dags:[{...}], producing_tasks:[], consuming_tasks:[], aliases:[], watchers:[], last_asset_event:{...}}], total_entries:int}
```

### assets_events
```
{asset_events:[{id:int, asset_id:int, uri:str, name:str, group:str, extra:{...}, source_task_id:str, source_dag_id:str, source_run_id:str, source_map_index:int, created_dagruns:[], timestamp:str, partition_key:null}], total_entries:int}
```

### config
```
{fallback_page_limit:int, auto_refresh_interval:int, hide_paused_dags_by_default:bool, instance_name:str, enable_swagger_ui:bool, require_confirmation_dag_change:bool, default_wrap:bool, test_connection:str, dashboard_alert:[], show_external_log_redirect:bool, external_log_name:null, theme:null, multi_team:bool}
```

### dag
```
{dag_id:str, dag_display_name:str, is_paused:bool, is_stale:bool, last_parsed_time:str, last_parse_duration:float, last_expired:null, bundle_name:str, bundle_version:null, relative_fileloc:str, fileloc:str, description:null, timetable_summary:str, timetable_description:str, timetable_partitioned:bool, tags:[{name:str, dag_id:str, dag_display_name:str}], max_active_tasks:int, max_active_runs:int, max_consecutive_failed_dag_runs:int, has_task_concurrency_limits:bool, has_import_errors:bool, next_dagrun_logical_date:str, next_dagrun_data_interval_start:str, next_dagrun_data_interval_end:str, next_dagrun_run_after:str, allowed_run_types:null, owners:[str], file_token:str}
```

### dagRun_single
```
{dag_run_id:str, dag_id:str, logical_date:null, queued_at:str, start_date:str, end_date:str, duration:float, data_interval_start:null, data_interval_end:null, run_after:str, last_scheduling_decision:str, run_type:str, state:str, triggered_by:str, triggering_user_name:str, conf:{}, note:null, dag_versions:[{id:str, version_number:int, dag_id:str, bundle_name:str, bundle_version:null, created_at:str, dag_display_name:str, bundle_url:null}], bundle_version:null, dag_display_name:str, partition_key:null}
```

### dagRuns
```
{dag_runs:[{dag_run_id:str, dag_id:str, logical_date:null, queued_at:str, start_date:str, end_date:str, duration:float, data_interval_start:null, data_interval_end:null, run_after:str, last_scheduling_decision:str, run_type:str, state:str, triggered_by:str, triggering_user_name:str, conf:{...}, note:null, dag_versions:[{...}], bundle_version:null, dag_display_name:str, partition_key:null}], total_entries:int}
```

### dagSource
```
{content:str, dag_id:str, version_number:int, dag_display_name:str}
```

### dag_details
```
{dag_id:str, dag_display_name:str, is_paused:bool, is_stale:bool, last_parsed_time:str, last_parse_duration:float, last_expired:null, bundle_name:str, bundle_version:null, relative_fileloc:str, fileloc:str, description:null, timetable_summary:str, timetable_description:str, timetable_partitioned:bool, tags:[{name:str, dag_id:str, dag_display_name:str}], max_active_tasks:int, max_active_runs:int, max_consecutive_failed_dag_runs:int, has_task_concurrency_limits:bool, has_import_errors:bool, next_dagrun_logical_date:str, next_dagrun_data_interval_start:str, next_dagrun_data_interval_end:str, next_dagrun_run_after:str, allowed_run_types:null, owners:[str], catchup:bool, dag_run_timeout:str, asset_expression:null, doc_md:str, start_date:str, end_date:null, is_paused_upon_creation:null, params:{example_key:{value:str, schema:{...}, description:null, source:null}}, render_template_as_native_obj:bool, template_search_path:null, timezone:str, last_parsed:str, default_args:{}}
```

### dag_tasks
```
{tasks:[{task_id:str, task_display_name:str, owner:str, start_date:str, end_date:null, trigger_rule:str, depends_on_past:bool, wait_for_downstream:bool, retries:float, queue:str, pool:str, pool_slots:float, execution_timeout:null, retry_delay:{...}, retry_exponential_backoff:float, priority_weight:float, weight_rule:str, ui_color:str, ui_fgcolor:str, template_fields:[str], downstream_task_ids:[str], doc_md:null, operator_name:str, params:{...}, class_ref:{...}, is_mapped:bool, extra_links:[]}], total_entries:int}
```

### dashboard_dag_stats
```
{active_dag_count:int, failed_dag_count:int, running_dag_count:int, queued_dag_count:int}
```

### dashboard_historical
```
{dag_run_states:{queued:int, running:int, success:int, failed:int}, task_instance_states:{no_status:int, removed:int, scheduled:int, queued:int, running:int, success:int, restarting:int, failed:int, up_for_retry:int, up_for_reschedule:int, upstream_failed:int, skipped:int, deferred:int}, state_count_limit:int}
```

### eventLogs
```
{event_logs:[{event_log_id:int, when:str, dag_id:str, task_id:str, run_id:str, map_index:int, try_number:int, event:str, logical_date:null, owner:str, extra:str, dag_display_name:str, task_display_name:str}], total_entries:int}
```

### importErrors
```
{import_errors:[], total_entries:int}
```

### monitor_health
```
{metadatabase:{status:str}, scheduler:{status:str, latest_scheduler_heartbeat:str}, triggerer:{status:str, latest_triggerer_heartbeat:str}, dag_processor:{status:str, latest_dag_processor_heartbeat:str}}
```

### plugins
```
{plugins:[{name:str, macros:[], flask_blueprints:[], fastapi_apps:[], fastapi_root_middlewares:[], external_views:[], react_apps:[], appbuilder_views:[], appbuilder_menu_items:[], global_operator_extra_links:[], operator_extra_links:[], source:str, listeners:[str], timetables:[]}], total_entries:int}
```

### pools
```
{pools:[{name:str, slots:int, description:str, include_deferred:bool, occupied_slots:int, running_slots:int, queued_slots:int, scheduled_slots:int, open_slots:int, deferred_slots:int, team_name:null}], total_entries:int}
```

### task
```
{task_id:str, task_display_name:str, owner:str, start_date:str, end_date:null, trigger_rule:str, depends_on_past:bool, wait_for_downstream:bool, retries:float, queue:str, pool:str, pool_slots:float, execution_timeout:null, retry_delay:{__type:str, days:int, seconds:int, microseconds:int}, retry_exponential_backoff:float, priority_weight:float, weight_rule:str, ui_color:str, ui_fgcolor:str, template_fields:[str], downstream_task_ids:[str], doc_md:null, operator_name:str, params:{example_key:{value:str, schema:{...}, description:null, source:str}}, class_ref:{module_path:str, class_name:str}, is_mapped:bool, extra_links:[]}
```

### taskInstance_single
```
{id:str, task_id:str, dag_id:str, dag_run_id:str, map_index:int, logical_date:null, run_after:str, start_date:str, end_date:str, duration:float, state:str, try_number:int, max_tries:int, task_display_name:str, dag_display_name:str, hostname:str, unixname:str, pool:str, pool_slots:int, queue:str, priority_weight:int, operator:str, operator_name:str, queued_when:str, scheduled_when:str, pid:int, executor:null, executor_config:str, note:null, rendered_map_index:null, rendered_fields:{bash_command:str, env:null, cwd:null}, trigger:null, triggerer_job:null, dag_version:{id:str, version_number:int, dag_id:str, bundle_name:str, bundle_version:null, created_at:str, dag_display_name:str, bundle_url:null}}
```

### taskInstances
```
{task_instances:[{id:str, task_id:str, dag_id:str, dag_run_id:str, map_index:int, logical_date:null, run_after:str, start_date:str, end_date:str, duration:float, state:str, try_number:int, max_tries:int, task_display_name:str, dag_display_name:str, hostname:str, unixname:str, pool:str, pool_slots:int, queue:str, priority_weight:int, operator:str, operator_name:str, queued_when:null, scheduled_when:null, pid:null, executor:null, executor_config:str, note:null, rendered_map_index:null, rendered_fields:{...}, trigger:null, triggerer_job:null, dag_version:{...}}], total_entries:int}
```

### ti_logs_json
```
{content:[{event:str, sources:[str]}], continuation_token:null}
```

### ti_tries
```
{task_instances:[{task_id:str, dag_id:str, dag_run_id:str, map_index:int, start_date:str, end_date:str, duration:float, state:str, try_number:int, max_tries:int, task_display_name:str, dag_display_name:str, hostname:str, unixname:str, pool:str, pool_slots:int, queue:str, priority_weight:int, operator:str, operator_name:str, queued_when:str, scheduled_when:str, pid:int, executor:null, executor_config:str, dag_version:{...}}], total_entries:int}
```

### ti_xcom
```
{xcom_entries:[{key:str, timestamp:str, logical_date:null, map_index:int, task_id:str, dag_id:str, run_id:str, dag_display_name:str, task_display_name:str, run_after:str}], total_entries:int}
```

### ui_dags
```
{total_entries:int, dags:[{dag_id:str, dag_display_name:str, is_paused:bool, is_stale:bool, last_parsed_time:str, last_parse_duration:float, bundle_name:str, relative_fileloc:str, fileloc:str, timetable_summary:str, timetable_description:str, timetable_partitioned:bool, tags:[{...}], max_active_tasks:int, max_active_runs:int, max_consecutive_failed_dag_runs:int, has_task_concurrency_limits:bool, has_import_errors:bool, owners:[str], asset_expression:{...}, latest_dag_runs:[], pending_actions:[], is_favorite:bool, file_token:str}]}
```

### ui_grid_runs
```
[{dag_id:str, run_id:str, queued_at:str, start_date:str, end_date:str, run_after:str, state:str, run_type:str, dag_versions:[{...}], has_missed_deadline:bool, duration:float}]
```

### ui_grid_structure
```
[{id:str, label:str}]
```

### ui_grid_ti_summaries
```
{run_id:str, dag_id:str, task_instances:[{task_id:str, task_display_name:str, state:str, child_states:null, min_start_date:str, max_end_date:str, dag_version_number:int}]}
```

### ui_latest_run
```
{id:int, dag_id:str, run_id:str, logical_date:null, run_after:str, start_date:str, end_date:str, state:str, duration:float}
```

### ui_structure_data
```
{edges:[{source_id:str, target_id:str, is_setup_teardown:null, label:null, is_source_asset:null}], nodes:[{id:str, label:str, type:str, children:null, is_mapped:null, tooltip:null, setup_teardown_type:null, operator:str, asset_condition_type:null}]}
```

### version
```
{version:str, git_version:str}
```
