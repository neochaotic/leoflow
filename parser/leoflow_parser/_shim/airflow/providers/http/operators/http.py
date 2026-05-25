from airflow._core import BaseOperator


class HttpOperator(BaseOperator):
    """Name carries 'Http' -> Leoflow 'http_api'; reads .method/.endpoint/.headers."""
